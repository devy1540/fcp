package state

import (
	"bytes"
	"errors"
	"testing"
)

func TestMultipartValidationListingAndReplacementEdges(t *testing.T) {
	store := openEdgeStore(t)
	if _, err := store.CreateMultipartUpload("missing", "key", "", nil); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing multipart bucket error=%v", err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	upload, err := store.CreateMultipartUpload("assets", "path/file", "text/plain", map[string]string{"env": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if cloneMultipartUpload(nil) != nil {
		t.Fatal("nil multipart upload clone should remain nil")
	}
	if _, err := store.UploadMultipartPart("assets", "path/file", upload.ID, 0, nil); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("invalid low part number error=%v", err)
	}
	if _, err := store.UploadMultipartPart("assets", "path/file", upload.ID, 10_001, nil); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("invalid high part number error=%v", err)
	}
	if _, err := store.UploadMultipartPart("assets", "wrong", upload.ID, 1, nil); !errors.Is(err, ErrMultipartUploadNotFound) {
		t.Fatalf("mismatched upload path error=%v", err)
	}
	first, err := store.UploadMultipartPart("assets", "path/file", upload.ID, 1, []byte("small"))
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := store.UploadMultipartPart("assets", "path/file", upload.ID, 1, []byte("replacement"))
	if err != nil || replaced.File == first.File {
		t.Fatalf("part replacement failed: first=%+v replaced=%+v err=%v", first, replaced, err)
	}
	second, err := store.UploadMultipartPart("assets", "path/file", upload.ID, 2, bytes.Repeat([]byte("x"), 5*1024*1024))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ListMultipartParts("assets", "wrong", upload.ID); !errors.Is(err, ErrMultipartUploadNotFound) {
		t.Fatalf("mismatched list parts error=%v", err)
	}
	if _, err := store.ListMultipartUploads("missing", ""); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing multipart list bucket error=%v", err)
	}
	uploads, err := store.ListMultipartUploads("assets", "path/")
	if err != nil || len(uploads) != 1 || uploads[0].ID != upload.ID {
		t.Fatalf("unexpected multipart uploads: %+v err=%v", uploads, err)
	}
	if uploads, err := store.ListMultipartUploads("assets", "other"); err != nil || len(uploads) != 0 {
		t.Fatalf("prefix should filter multipart uploads: %+v err=%v", uploads, err)
	}

	if _, err := store.CompleteMultipartUpload("assets", "wrong", upload.ID, nil); !errors.Is(err, ErrMultipartUploadNotFound) {
		t.Fatalf("mismatched completion error=%v", err)
	}
	if _, err := store.CompleteMultipartUpload("assets", "path/file", upload.ID, nil); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("empty completion error=%v", err)
	}
	if _, err := store.CompleteMultipartUpload("assets", "path/file", upload.ID, []CompletedMultipartPart{
		{PartNumber: 2, ETag: second.ETag},
		{PartNumber: 1, ETag: replaced.ETag},
	}); !errors.Is(err, ErrInvalidPartOrder) {
		t.Fatalf("unordered completion error=%v", err)
	}
	if _, err := store.CompleteMultipartUpload("assets", "path/file", upload.ID, []CompletedMultipartPart{{PartNumber: 1, ETag: "wrong"}}); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("bad ETag completion error=%v", err)
	}
	if _, err := store.CompleteMultipartUpload("assets", "path/file", upload.ID, []CompletedMultipartPart{
		{PartNumber: 1, ETag: replaced.ETag},
		{PartNumber: 2, ETag: second.ETag},
	}); !errors.Is(err, ErrEntityTooSmall) {
		t.Fatalf("small non-final part error=%v", err)
	}
	if err := store.AbortMultipartUpload("assets", "wrong", upload.ID); !errors.Is(err, ErrMultipartUploadNotFound) {
		t.Fatalf("mismatched abort error=%v", err)
	}
	if err := store.AbortMultipartUpload("assets", "path/file", upload.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortMultipartUpload("assets", "path/file", upload.ID); !errors.Is(err, ErrMultipartUploadNotFound) {
		t.Fatalf("second abort error=%v", err)
	}
}

func TestObjectListingNotificationMatchingAndErrors(t *testing.T) {
	store := openEdgeStore(t)
	if _, _, err := store.ListObjects("missing", "", "", 1); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing list bucket error=%v", err)
	}
	if _, err := store.PutObject("missing", "key", nil, "", nil); !errors.Is(err, ErrBucketNotFound) {
		t.Fatalf("missing put bucket error=%v", err)
	}
	if err := store.CreateBucket("assets"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b", "prefix/c"} {
		if _, err := store.PutObject("assets", key, []byte(key), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	objects, truncated, err := store.ListObjects("assets", "", "", 1)
	if err != nil || len(objects) != 1 || !truncated || objects[0].Key != "a" {
		t.Fatalf("unexpected first object page: objects=%+v truncated=%v err=%v", objects, truncated, err)
	}
	objects, truncated, err = store.ListObjects("assets", "", "a", 10)
	if err != nil || len(objects) != 2 || truncated {
		t.Fatalf("unexpected continuation page: objects=%+v truncated=%v err=%v", objects, truncated, err)
	}
	if !matchesObjectCreated([]string{"s3:ObjectCreated:*"}, "ObjectCreated:Put") ||
		!matchesObjectCreated([]string{"s3:ObjectCreated:Put"}, "ObjectCreated:Put") ||
		matchesObjectCreated([]string{"s3:ObjectRemoved:*"}, "ObjectCreated:Put") {
		t.Fatal("S3 notification event matching is incorrect")
	}
}
