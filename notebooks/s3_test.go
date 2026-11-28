package notebooks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3ObjectStoreScopesOperationsToNotebookPrefix(t *testing.T) {
	const notebookID = "28c81446-d031-427d-b129-f981173f2001"
	const objectID = "1dfd51b8-4c46-45bd-b2d7-24aed45bdd3a"
	const expectedPath = "/notes-bucket/notebooks/" + notebookID + "/" + objectID
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != expectedPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, expectedPath)
		}
		switch r.Method {
		case http.MethodPut:
			if got := r.Header.Get("Content-Type"); got != "text/plain" {
				t.Errorf("Content-Type = %q, want text/plain", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request body: %v", err)
			}
			if string(body) != "blob bytes" {
				t.Errorf("PUT body = %q", body)
			}
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			w.Header().Set("Content-Length", "10")
			_, _ = io.WriteString(w, "blob bytes")
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test-key", "test-secret", ""),
		HTTPClient:  server.Client(),
	}
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(server.URL)
		options.UsePathStyle = true
	})
	store := &S3ObjectStore{client: client, bucket: "notes-bucket"}

	if err := store.Put(context.Background(), notebookID, objectID, "text/plain", 10, strings.NewReader("blob bytes")); err != nil {
		t.Fatal(err)
	}
	body, err := store.Get(context.Background(), notebookID, objectID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "blob bytes" {
		t.Fatalf("GET body = %q", got)
	}
	if err := store.Delete(context.Background(), notebookID, objectID); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 || methods[0] != http.MethodPut || methods[1] != http.MethodGet || methods[2] != http.MethodDelete {
		t.Fatalf("request methods = %#v", methods)
	}
}

func TestS3ObjectStoreRejectsObjectsOutsideNotebookScope(t *testing.T) {
	if _, err := objectKey("../../another-notebook", "1dfd51b8-4c46-45bd-b2d7-24aed45bdd3a"); err == nil {
		t.Fatal("objectKey accepted an invalid notebook scope")
	}
	if _, err := objectKey("28c81446-d031-427d-b129-f981173f2001", "../other-object"); err == nil {
		t.Fatal("objectKey accepted an invalid object ID")
	}
}
