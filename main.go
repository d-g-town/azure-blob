package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"cloud.google.com/go/storage"
)

type Store interface {
	Get(ctx context.Context) (io.ReadCloser, error)
	Put(ctx context.Context, data []byte) error
}

var errNotFound = errors.New("not found")

func main() {
	provider := strings.ToLower(os.Getenv("STORAGE_PROVIDER"))
	if provider == "" {
		provider = "azure"
	}

	ctx := context.Background()

	var store Store
	var err error
	switch provider {
	case "azure":
		store, err = newAzureStore(ctx)
	case "gcp", "gcs":
		store, err = newGCSStore(ctx)
	default:
		log.Fatalf("unsupported STORAGE_PROVIDER %q (expected azure or gcp)", provider)
	}
	if err != nil {
		log.Fatalf("failed to init %s store: %v", provider, err)
	}

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/api/note", func(w http.ResponseWriter, r *http.Request) {
		handleNote(w, r, store)
	})

	addr := ":8080"
	log.Printf("listening on %s (provider=%s)", addr, provider)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func handleNote(w http.ResponseWriter, r *http.Request, store Store) {
	ctx := r.Context()

	switch r.Method {
	case http.MethodGet:
		rc, err := store.Get(ctx)
		if err != nil {
			if errors.Is(err, errNotFound) {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "text/plain")
		io.Copy(w, rc)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.Put(ctx, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

type azureStore struct {
	client    *azblob.Client
	container string
	blob      string
}

func newAzureStore(ctx context.Context) (*azureStore, error) {
	account := os.Getenv("AZURE_STORAGE_ACCOUNT")
	if account == "" {
		return nil, errors.New("AZURE_STORAGE_ACCOUNT is required")
	}
	container := envOr("AZURE_CONTAINER_NAME", "notes")
	blob := envOr("AZURE_BLOB_NAME", "note.txt")

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("create credential: %w", err)
	}
	client, err := azblob.NewClient(fmt.Sprintf("https://%s.blob.core.windows.net/", account), cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create blob client: %w", err)
	}
	if _, err := client.CreateContainer(ctx, container, nil); err != nil && !strings.Contains(err.Error(), "ContainerAlreadyExists") {
		log.Printf("warning: could not ensure container exists: %v", err)
	}
	return &azureStore{client: client, container: container, blob: blob}, nil
}

func (s *azureStore) Get(ctx context.Context) (io.ReadCloser, error) {
	resp, err := s.client.DownloadStream(ctx, s.container, s.blob, nil)
	if err != nil {
		if strings.Contains(err.Error(), "BlobNotFound") {
			return nil, errNotFound
		}
		return nil, err
	}
	return resp.Body, nil
}

func (s *azureStore) Put(ctx context.Context, data []byte) error {
	_, err := s.client.UploadBuffer(ctx, s.container, s.blob, data, nil)
	return err
}

type gcsStore struct {
	obj *storage.ObjectHandle
}

func newGCSStore(ctx context.Context) (*gcsStore, error) {
	bucket := os.Getenv("GCS_BUCKET")
	if bucket == "" {
		return nil, errors.New("GCS_BUCKET is required")
	}
	object := envOr("GCS_OBJECT_NAME", "note.txt")

	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create storage client: %w", err)
	}
	return &gcsStore{obj: client.Bucket(bucket).Object(object)}, nil
}

func (s *gcsStore) Get(ctx context.Context) (io.ReadCloser, error) {
	rc, err := s.obj.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, errNotFound
		}
		return nil, err
	}
	return rc, nil
}

func (s *gcsStore) Put(ctx context.Context, data []byte) error {
	w := s.obj.NewWriter(ctx)
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Cloud Notes</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; max-width: 640px; margin: 40px auto; padding: 0 16px; }
  h1 { margin-bottom: 16px; font-size: 1.4rem; }
  textarea { width: 100%; height: 300px; padding: 12px; font-size: 1rem; border: 1px solid #ccc; border-radius: 6px; resize: vertical; }
  #status { margin-top: 8px; font-size: 0.85rem; color: #666; }
</style>
</head>
<body>
  <h1>Cloud Notes</h1>
  <textarea id="editor" placeholder="Type here..."></textarea>
  <div id="status">Loading...</div>
  <script>
    const editor = document.getElementById('editor');
    const status = document.getElementById('status');
    let timer = null;

    fetch('/api/note')
      .then(r => r.text())
      .then(t => { editor.value = t; status.textContent = 'Loaded.'; })
      .catch(e => { status.textContent = 'Failed to load: ' + e; });

    editor.addEventListener('input', () => {
      status.textContent = 'Saving...';
      clearTimeout(timer);
      timer = setTimeout(() => {
        fetch('/api/note', { method: 'PUT', body: editor.value })
          .then(r => {
            status.textContent = r.ok ? 'Saved.' : 'Error saving.';
          })
          .catch(e => { status.textContent = 'Error: ' + e; });
      }, 500);
    });
  </script>
</body>
</html>`
