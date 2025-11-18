package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/kcp-dev/kcp/gardener/runner/options"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

type Server struct {
	Config *options.Serve

	DynamicClient dynamic.Interface

	WebhookServer *http.Server
}

func NewServer(ctx context.Context, targetRest *rest.Config, config *options.Serve) (*Server, error) {
	s := &Server{
		Config: config,
	}

	var err error
	if s.DynamicClient, err = dynamic.NewForConfig(targetRest); err != nil {
		return nil, err
	}

	// Setup webhook server
	mux := http.NewServeMux()
	mux.HandleFunc("/validate-shoot", s.handleValidateShoot)

	server := &http.Server{
		Addr:         config.ListenAddress,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Configure TLS if certificates are provided
	if config.CertFile != "" && config.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificates: %v", err)
		}

		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	s.WebhookServer = server

	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	if s.WebhookServer.TLSConfig != nil {
		logger.Info("Starting HTTPS webhook server", "addr", s.WebhookServer.Addr)
		logger.Info("Using TLS certificates", "cert", s.Config.CertFile, "key", s.Config.KeyFile)
		if err := s.WebhookServer.ServeTLS(s.Config.Listener, s.Config.CertFile, s.Config.KeyFile); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "Failed to start HTTPS webhook server")
			return err
		}
	}

	// Wait for context cancellation
	<-ctx.Done()
	logger.Info("Shutting down webhook server...")

	// Create a context with timeout for graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.WebhookServer.Shutdown(shutdownCtx); err != nil {
		logger.Error(err, "Server forced to shutdown")
		return err
	}

	logger.Info("Server gracefully stopped")
	return nil
}

func (s *Server) handleValidateShoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		klog.Errorf("Error reading request body: %v", err)
		return
	}
	defer r.Body.Close()

	// Parse the admission request
	var admissionReview admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReview); err != nil {
		http.Error(w, "Failed to parse admission request", http.StatusBadRequest)
		klog.Errorf("Error parsing admission request: %v", err)
		return
	}

	req := admissionReview.Request
	if req == nil {
		http.Error(w, "Missing admission request", http.StatusBadRequest)
		return
	}

	klog.Infof("Received validation request for %s/%s in namespace %s", req.Kind.Kind, req.Name, req.Namespace)

	// Create a dummy validation response - always allow for now
	allowed := true
	message := "Validation passed - dummy webhook"

	// You can add custom validation logic here based on req.Object
	if req.Object.Raw != nil {
		klog.V(4).Infof("Object data received: %s", string(req.Object.Raw))

		// Example: Parse the object and perform validation
		var obj *unstructured.Unstructured
		if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
			allowed = false
			message = fmt.Sprintf("Failed to parse object: %v", err)
		}

		client := s.DynamicClient.Resource(schema.GroupVersionResource{
			Group:    obj.GroupVersionKind().Group,
			Resource: strings.ToLower(obj.GetKind() + "s"),
			Version:  obj.GroupVersionKind().Version,
		})
		// DO a get to see if object does not exists - dry run on existing object passes

		_, err := client.Namespace(obj.GetNamespace()).Get(r.Context(), obj.GetName(), metav1.GetOptions{})
		if err == nil {
			allowed = false
			message = "Object already exists"
		} else {

			klog.V(4).Infof("Parsed object: %s", spew.Sdump(obj))
			_, err = client.Namespace(obj.GetNamespace()).Create(r.Context(), obj, metav1.CreateOptions{
				DryRun: []string{
					metav1.DryRunAll,
				},
			})
			if err != nil {
				allowed = false
				message = fmt.Sprintf("Dynamic client create failed: %v", err)
			}
		}

		if allowed {
			message = "Validation passed - object accepted"
		}
	}

	// Create the admission response
	admissionResponse := &admissionv1.AdmissionResponse{
		UID:     req.UID,
		Allowed: allowed,
		Result: &metav1.Status{
			Message: message,
		},
	}

	// Create the admission review response
	admissionReview.Response = admissionResponse
	admissionReview.Request = nil // Clear request for response

	// Set the API version and kind
	admissionReview.APIVersion = "admission.k8s.io/v1"
	admissionReview.Kind = "AdmissionReview"

	// Marshal the response
	respBytes, err := json.Marshal(admissionReview)
	if err != nil {
		http.Error(w, "Failed to marshal response", http.StatusInternalServerError)
		klog.Errorf("Error marshaling response: %v", err)
		return
	}

	// Send the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(respBytes); err != nil {
		klog.Errorf("Error writing response: %v", err)
	}

	klog.Infof("Sent validation response: allowed=%v, message=%s", allowed, message)
}
