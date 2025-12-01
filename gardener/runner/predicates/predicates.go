package predicates

import (
	"sync"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type filter func(object *unstructured.Unstructured) bool

type Registry interface {
	AddOrUpdatePredicate(name string, filter filter) error
	DeletePredicate(name string, filter filter) error

	GetPredicates() map[string]filter
}

type registry struct {
	lock  sync.Mutex
	funcs map[string]filter
}

func New() Registry {
	r := &registry{
		funcs: make(map[string]filter),
	}

	return r
}

func (r *registry) AddOrUpdatePredicate(name string, f filter) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	r.funcs[name] = f
	return nil
}

func (r *registry) DeletePredicate(name string, f filter) error {
	r.lock.Lock()
	defer r.lock.Unlock()

	delete(r.funcs, name)
	return nil
}

func (r *registry) GetPredicates() map[string]filter {
	r.lock.Lock()
	defer r.lock.Unlock()

	result := make(map[string]filter)
	for name, f := range r.funcs {
		result[name] = f
	}
	return result
}
