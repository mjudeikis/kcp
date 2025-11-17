package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func main() {
	var (
		port     = flag.Int("port", 9443, "Webhook server port")
		certFile = flag.String("tls-cert-file", "", "File containing the x509 Certificate for HTTPS. (CA cert, if any, concatenated after server cert)")
		keyFile  = flag.String("tls-private-key-file", "", "File containing the x509 private key for HTTPS")
		insecure = flag.Bool("insecure", false, "Run server without TLS (HTTP instead of HTTPS)")
	)
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/validate-shoot", handleValidateShoot)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: mux,
	}

	// Configure TLS if not running in insecure mode
	if !*insecure {
		if *certFile == "" || *keyFile == "" {
			log.Fatal("TLS cert and key files are required when not running in insecure mode. Use --insecure flag for HTTP or provide --tls-cert-file and --tls-private-key-file")
		}

		// Load TLS certificates
		cert, err := tls.LoadX509KeyPair(*certFile, *keyFile)
		if err != nil {
			log.Fatalf("Failed to load TLS certificates: %v", err)
		}

		server.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	}

	// Channel to listen for interrupt signal to terminate gracefully
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if *insecure {
			log.Printf("Starting HTTP webhook server on %s", server.Addr)
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Failed to start server: %v", err)
			}
		} else {
			log.Printf("Starting HTTPS webhook server on %s", server.Addr)
			log.Printf("Using TLS cert: %s, key: %s", *certFile, *keyFile)
			if err := server.ListenAndServeTLS(*certFile, *keyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Failed to start server: %v", err)
			}
		}
	}()

	// Wait for interrupt signal
	<-stop
	log.Println("Shutting down webhook server...")

	// Create a context with timeout for graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	log.Println("Server gracefully stopped")
}

func handleValidateShoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		log.Printf("Error reading request body: %v", err)
		return
	}
	defer r.Body.Close()

	// Parse the admission request
	var admissionReview admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &admissionReview); err != nil {
		http.Error(w, "Failed to parse admission request", http.StatusBadRequest)
		log.Printf("Error parsing admission request: %v", err)
		return
	}

	req := admissionReview.Request
	if req == nil {
		http.Error(w, "Missing admission request", http.StatusBadRequest)
		return
	}

	log.Printf("Received validation request for %s/%s in namespace %s", req.Kind.Kind, req.Name, req.Namespace)

	// Create a dummy validation response - always allow for now
	allowed := true
	message := "Validation passed - dummy webhook"

	// You can add custom validation logic here based on req.Object
	if req.Object.Raw != nil {
		log.Printf("Object data received: %s", string(req.Object.Raw))
		
		// Example: Parse the object and perform validation
		var obj map[string]any
		if err := json.Unmarshal(req.Object.Raw, &obj); err == nil {
			// Perform some dummy validation
			if metadata, ok := obj["metadata"].(map[string]any); ok {
				if name, ok := metadata["name"].(string); ok && name == "invalid-shoot" {
					allowed = false
					message = "Shoot name 'invalid-shoot' is not allowed"
				}
			}
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
		log.Printf("Error marshaling response: %v", err)
		return
	}

	// Send the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(respBytes); err != nil {
		log.Printf("Error writing response: %v", err)
	}

	log.Printf("Sent validation response: allowed=%v, message=%s", allowed, message)
}