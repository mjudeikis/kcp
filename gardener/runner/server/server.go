package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/gardener/gardener/pkg/apis/core/v1beta1"
	"github.com/kcp-dev/kcp/gardener/runner/options"
	"github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func init() {
	v1beta1.AddToScheme(scheme.Scheme)
}

type Server struct {
	Config *options.Serve

	client client.Client

	WebhookServer *http.Server
}

func NewServer(ctx context.Context, targetRest *rest.Config, config *options.Serve) (*Server, error) {
	s := &Server{
		Config: config,
	}

	var err error
	if s.client, err = client.New(targetRest, client.Options{
		Scheme: scheme.Scheme,
	}); err != nil {
		return nil, err
	}

	// Setup webhook server
	mux := http.NewServeMux()
	mux.HandleFunc("/validate-shoot", s.handleValidateShoot)
	mux.HandleFunc("/mutate-shoot", s.handleMutateShoot)

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
		var obj *v1beta1.Shoot
		if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
			allowed = false
			message = fmt.Sprintf("Failed to parse object: %v", err)
		}

		err := s.client.Get(r.Context(), client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}, obj)
		if err == nil {
			allowed = false
			message = "Object already exists"
		} else {

			klog.V(4).Infof("Parsed object: %s", spew.Sdump(obj))
			err = s.client.Create(r.Context(), obj, &client.CreateOptions{DryRun: []string{"All"}})
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

func (s *Server) handleMutateShoot(w http.ResponseWriter, r *http.Request) {
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

	klog.Infof("Received mutation request for %s/%s in namespace %s", req.Kind.Kind, req.Name, req.Namespace)

	// Create admission response - always allow for now
	allowed := true
	message := "Mutation passed - dummy webhook"

	// You can add custom mutation logic here based on req.Object
	if req.Object.Raw != nil {
		klog.V(4).Infof("Object data received: %s", string(req.Object.Raw))

		// Example: Parse the object and perform mutation via proxy to provider cluster
		var obj *v1beta1.Shoot
		if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
			allowed = false
			message = fmt.Sprintf("Failed to parse object: %v", err)
		} else {
			// Check if object already exists - dry run on existing object passes
			err := s.client.Get(r.Context(), client.ObjectKey{Namespace: obj.Namespace, Name: obj.Name}, obj)
			if err == nil {
				allowed = false
				message = "Object already exists"
			} else {
				klog.V(4).Infof("Parsed object: %s", spew.Sdump(obj))
				err = s.client.Create(r.Context(), obj, &client.CreateOptions{DryRun: []string{"All"}})
				if err != nil {
					allowed = false
					message = fmt.Sprintf("Dynamic client create failed: %v", err)
				}
			}

			if allowed {
				message = "Mutation passed - object accepted"
			}
		}
		spew.Dump("Mutation response object", obj)
	}

	// Create the admission response
	admissionResponse := &admissionv1.AdmissionResponse{
		UID:     req.UID,
		Allowed: true,
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

	spew.Dump(string(respBytes))
	klog.Infof("Sent mutation response: allowed=%v, message=%s", allowed, message)
}
