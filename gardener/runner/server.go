/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package runner

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
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

type Server struct {
	Config *Config
}

func NewServer(ctx context.Context, config *Config) (*Server, error) {
	s := &Server{
		Config: config,
	}

	// Setup webhook server
	mux := http.NewServeMux()
	mux.HandleFunc("/validate-shoot", s.handleValidateShoot)

	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", config.Options.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Configure TLS if certificates are provided
	if config.Options.CertFile != "" && config.Options.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(config.Options.CertFile, config.Options.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificates: %v", err)
		}

		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	config.WebhookServer = server

	return s, nil
}

func (s *Server) Start(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	// Start the webhook server
	go func() {
		if s.Config.WebhookServer.TLSConfig != nil {
			logger.Info("Starting HTTPS webhook server", "addr", s.Config.WebhookServer.Addr)
			logger.Info("Using TLS certificates", "cert", s.Config.Options.CertFile, "key", s.Config.Options.KeyFile)
			if err := s.Config.WebhookServer.ListenAndServeTLS(s.Config.Options.CertFile, s.Config.Options.KeyFile); err != nil && err != http.ErrServerClosed {
				logger.Error(err, "Failed to start HTTPS server")
			}
		} else {
			logger.Info("Starting HTTP webhook server", "addr", s.Config.WebhookServer.Addr)
			if err := s.Config.WebhookServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error(err, "Failed to start HTTP server")
			}
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	logger.Info("Shutting down webhook server...")

	// Create a context with timeout for graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.Config.WebhookServer.Shutdown(shutdownCtx); err != nil {
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

		client := s.Config.DynamicClient.Resource(schema.GroupVersionResource{
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
