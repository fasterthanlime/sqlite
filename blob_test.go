// Copyright (c) 2018 David Crawshaw <david@zentus.com>
//
// Permission to use, copy, modify, and distribute this software for any
// purpose with or without fee is hereby granted, provided that the above
// copyright notice and this permission notice appear in all copies.
//
// THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
// WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
// MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
// ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
// WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
// ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
// OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.

package sqlite_test

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"crawshaw.io/sqlite"
)

func TestBlob(t *testing.T) {
	c, err := sqlite.OpenConn(":memory:", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()

	stmt, _, err := c.PrepareTransient("CREATE TABLE blobs (col BLOB);")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	stmt, _, err = c.PrepareTransient("INSERT INTO blobs (col) VALUES ($col);")
	if err != nil {
		t.Fatal(err)
	}
	stmt.SetZeroBlob("$col", 5)
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}
	rowid := c.LastInsertRowID()
	t.Logf("blobs rowid: %d", rowid)

	b, err := c.OpenBlob("", "blobs", "col", rowid, true)
	if err != nil {
		t.Fatal(err)
	}
	n, err := b.WriteAt([]byte{1, 2, 3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("b.WriteAt n=%d, want 3", n)
	}
	n, err = b.WriteAt([]byte{2, 3, 4, 5}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("b.WriteAt n=%d, want 4", n)
	}
	if size := b.Size(); size != 5 {
		t.Fatalf("b.Size=%d, want 5", size)
	}
	n, err = b.WriteAt([]byte{2, 3, 4, 5, 6}, 1) // too long
	if err == nil {
		t.Fatalf("WriteAt too long, but no error")
	}
	if err := b.Close(); err != nil {
		t.Error(err)
	}

	b, err = c.OpenBlob("", "blobs", "col", rowid, false)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	n, err = b.ReadAt(got, 0)
	if n != len(got) {
		t.Fatalf("b.ReadAt=%d, want len(got)=%d", n, len(got))
	}

	want := []byte{1, 2, 3, 4, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("b.ReadAt got %v, want %v", got, want)
	}

	n, err = b.ReadAt(got[3:], 3)
	if n != len(got)-3 {
		t.Fatalf("b.ReadAt(got, 3)=%d, want len(got)-3=%d", n, len(got)-3)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("b.ReadAt(got, 3) %v, want %v", got, want)
	}
}

func TestConcurrentBlobSpins(t *testing.T) {
	c, err := sqlite.OpenConn("file::memory:?mode=memory", 0)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := sqlite.OpenConn("file::memory:?mode=memory", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer c2.Close()

	stmt, _, err := c.PrepareTransient("DROP TABLE IF EXISTS blobs;")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	stmt, _, err = c.PrepareTransient("CREATE TABLE blobs (col BLOB);")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	stmt, _, err = c.PrepareTransient("INSERT INTO blobs (col) VALUES ($col);")
	if err != nil {
		t.Fatal(err)
	}
	stmt.SetZeroBlob("$col", 1024)
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	blobRow1 := c.LastInsertRowID()
	if err := stmt.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	blobRow2 := c.LastInsertRowID()
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	blob1, err := c.OpenBlob("", "blobs", "col", blobRow1, true)
	if err != nil {
		t.Errorf("OpenBlob: %v", err)
		return
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		blob1.Close()
	}()

	countBefore := sqlite.ConnCount(c2)
	blob2, err := c2.OpenBlob("", "blobs", "col", blobRow2, true)
	countAfter := sqlite.ConnCount(c2)
	if err != nil {
		t.Errorf("OpenBlob: %v", err)
		return
	}
	blob2.Close()

	if spins := countAfter - countBefore - 1; spins > 1 {
		t.Errorf("expecting no more than 1 spin, got %d", spins)
	}
}

// TestConcurrentBlobWrites looks for unexpected SQLITE_LOCKED errors
// when using the (default) shared cache.
func TestConcurrentBlobWrites(t *testing.T) {
	const numBlobs = 5
	c, err := sqlite.OpenConn("file::memory:?mode=memory", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	stmt, _, err := c.PrepareTransient("DROP TABLE IF EXISTS blobs;")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	stmt, _, err = c.PrepareTransient("CREATE TABLE blobs (col BLOB);")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.Step(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	stmt, _, err = c.PrepareTransient("INSERT INTO blobs (col) VALUES ($col);")
	if err != nil {
		t.Fatal(err)
	}
	stmt.SetZeroBlob("$col", 1024)
	var blobRowIDs []int64
	for i := 0; i < numBlobs; i++ {
		if _, err := stmt.Step(); err != nil {
			t.Fatal(err)
		}
		blobRowIDs = append(blobRowIDs, c.LastInsertRowID())
		if err := stmt.Reset(); err != nil {
			t.Fatal(err)
		}
	}
	if err := stmt.Finalize(); err != nil {
		t.Error(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < numBlobs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := make([]byte, 1024)
			b[0] = byte(i)

			c, err := sqlite.OpenConn("file::memory:?mode=memory", 0)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()

			blob, err := c.OpenBlob("", "blobs", "col", blobRowIDs[i], true)
			if err != nil {
				t.Errorf("OpenBlob: %v (i=%d)", err, i)
				return
			}
			defer blob.Close()
			for j := 0; j < 10; j++ {
				b[1] = byte(j)

				n, err := blob.WriteAt(b, 0)
				if err != nil {
					t.Errorf("Blob.WriteAt: %v (i=%d, j=%d)", err, i, j)
					return
				}
				if n != len(b) {
					t.Fatalf("n=%d, want %d (i=%d, j=%d)", n, len(b), i, j)
				}
			}
		}(i)
	}
	wg.Wait()
}