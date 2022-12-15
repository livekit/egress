package service

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/livekit/egress/pkg/errors"
)

const (
	gstPipelineDotFile = "gst_pipeline"
)

type handerProxyHandler struct {
	processManager *ProcessManager
}

// URL path format is "/<application>/<egress_id>/<optional_other_params>"
func (p *handerProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pathElements := strings.Split(r.URL.Path, "/")
	if len(pathElements) < 3 {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("malformed url"))
		return
	}

	egressId := pathElements[2]

	port, err := p.processManager.getHandlerDebugPort(egressId)
	switch {
	case errors.Is(err, errors.ErrEgressNotFound):
		http.Error(w, "unkown egress", http.StatusNotFound)
		return
	case err == nil:
		// break
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if port == 0 {
		http.Error(w, "debug disabled for egress", http.StatusNotFound)
		return
	}

	// Remove the egressId from the URL
	pathElements = append(pathElements[:2], pathElements[3:]...)
	path := strings.Join(pathElements, "/")
	url := *r.URL
	url.Path = path

	urlString := fmt.Sprintf("http://::1:%d/%s", port, url.EscapedPath())
	if url.RawQuery != "" {
		urlString = fmt.Sprintf("%s?%s", urlString, url)
	}
	proxyResp, err := http.Get(urlString)
	if err != nil {
		// StatusInternalServerError may not always be the appropriate error code
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer proxyResp.Body.Close()

	_, err = io.Copy(w, proxyResp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type handlerDebugHandler struct {
	h *Handler
}

func (d *handlerDebugHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	dot, err := d.h.GetPipelineDebugInfo()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")

	w.Write(dot)
}
