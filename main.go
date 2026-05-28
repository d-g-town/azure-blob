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
		switch {
		case os.Getenv("GCS_BUCKET") != "":
			provider = "gcp"
		case os.Getenv("AZURE_STORAGE_ACCOUNT") != "":
			provider = "azure"
		default:
			log.Fatal("set STORAGE_PROVIDER=azure|gcp, or provide AZURE_STORAGE_ACCOUNT / GCS_BUCKET")
		}
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
  :root {
    --bg: #0f172a;
    --bg-grad: radial-gradient(circle at 20% 0%, #1e293b 0%, #0f172a 55%);
    --card: rgba(255, 255, 255, 0.04);
    --border: rgba(255, 255, 255, 0.08);
    --text: #e2e8f0;
    --muted: #94a3b8;
    --accent: #818cf8;
  }
  html, body { height: 100%; }
  body {
    font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
    background: var(--bg-grad);
    color: var(--text);
    min-height: 100vh;
    padding: 64px 16px;
    display: flex;
    justify-content: center;
  }
  main { width: 100%; max-width: 680px; }
  header { display: flex; align-items: baseline; justify-content: space-between; margin-bottom: 20px; }
  h1 {
    font-size: 1.5rem;
    font-weight: 600;
    letter-spacing: -0.02em;
    background: linear-gradient(90deg, #fff, #c7d2fe);
    -webkit-background-clip: text;
    background-clip: text;
    color: transparent;
  }
  .dot {
    display: inline-block;
    width: 8px; height: 8px;
    border-radius: 50%;
    background: var(--accent);
    margin-right: 8px;
    box-shadow: 0 0 12px var(--accent);
    vertical-align: middle;
  }
  .card {
    background: var(--card);
    border: 1px solid var(--border);
    border-radius: 14px;
    padding: 6px;
    backdrop-filter: blur(8px);
    box-shadow: 0 20px 60px -20px rgba(0, 0, 0, 0.5);
  }
  textarea {
    width: 100%;
    height: 360px;
    padding: 18px;
    font-size: 1rem;
    line-height: 1.6;
    font-family: ui-monospace, "SF Mono", Menlo, monospace;
    color: var(--text);
    background: transparent;
    border: none;
    border-radius: 10px;
    resize: vertical;
    outline: none;
  }
  textarea::placeholder { color: var(--muted); }
  #status {
    margin-top: 14px;
    font-size: 0.8rem;
    color: var(--muted);
    display: flex;
    align-items: center;
    gap: 8px;
  }
</style>
</head>
<body>
  <main>
    <header>
      <h1><span class="dot"></span>Cloud Notes</h1>
    </header>
    <div class="card">
      <textarea id="editor" placeholder="Start typing — your notes save automatically..."></textarea>
    </div>
    <div id="status">Loading...</div>
  </main>
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
