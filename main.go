package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
)

var client *azblob.Client

func main() {
	storageAccount := os.Getenv("AZURE_STORAGE_ACCOUNT")
	if storageAccount == "" {
		log.Fatal("AZURE_STORAGE_ACCOUNT environment variable is required")
	}
	containerName := os.Getenv("AZURE_CONTAINER_NAME")
	if containerName == "" {
		containerName = "notes"
	}
	blobName := os.Getenv("AZURE_BLOB_NAME")
	if blobName == "" {
		blobName = "note.txt"
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Fatalf("failed to create credential: %v", err)
	}

	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", storageAccount)
	client, err = azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		log.Fatalf("failed to create blob client: %v", err)
	}

	ctx := context.Background()
	_, err = client.CreateContainer(ctx, containerName, nil)
	if err != nil && !strings.Contains(err.Error(), "ContainerAlreadyExists") {
		log.Printf("warning: could not ensure container exists: %v", err)
	}

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/api/note", func(w http.ResponseWriter, r *http.Request) {
		handleNote(w, r, containerName, blobName)
	})

	addr := ":8080"
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func handleNote(w http.ResponseWriter, r *http.Request, container, blob string) {
	ctx := r.Context()

	switch r.Method {
	case http.MethodGet:
		resp, err := client.DownloadStream(ctx, container, blob, nil)
		if err != nil {
			if strings.Contains(err.Error(), "BlobNotFound") {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/plain")
		io.Copy(w, resp.Body)

	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, err = client.UploadBuffer(ctx, container, blob, body, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Azure Blob Notes</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: system-ui, sans-serif; max-width: 640px; margin: 40px auto; padding: 0 16px; }
  h1 { margin-bottom: 16px; font-size: 1.4rem; }
  textarea { width: 100%; height: 300px; padding: 12px; font-size: 1rem; border: 1px solid #ccc; border-radius: 6px; resize: vertical; }
  #status { margin-top: 8px; font-size: 0.85rem; color: #666; }
</style>
</head>
<body>
  <h1>Azure Blob Notes</h1>
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
