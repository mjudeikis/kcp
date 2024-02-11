/*
Copyright 2021 The KCP Authors.

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

package workspacemounts

import (
	"context"
	"sync"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

var tickerInterval = time.Second * 15

type mountStore struct {
	mountTrackerLock sync.Mutex
	mountTrackers    map[string]mountStoreEntry
	initOnce         sync.Once
	initDone         bool

	requeueWorkspace func(cluster logicalcluster.Path, workspace tenancyv1alpha1.Workspace) error
	getMountObject   func(ctx context.Context, cluster logicalcluster.Path, ref *v1.ObjectReference) (*unstructured.Unstructured, error)
}

type mountStoreEntry struct {
	cluster   logicalcluster.Path
	ref       *v1.ObjectReference
	workspace *tenancyv1alpha1.Workspace

	lastSeenURL   string
	lastSeenPhase tenancyv1alpha1.MountPhaseType
}

// Empty returns true if the entry is empty
func (m *mountStoreEntry) Empty() bool {
	return m.cluster.String() == "" || m.ref == nil || m.workspace == nil
}

func newMountsTrackerStore() *mountStore {
	return &mountStore{
		initOnce:         sync.Once{},
		mountTrackers:    make(map[string]mountStoreEntry),
		mountTrackerLock: sync.Mutex{},
	}
}

// init initializes the mount store. Bit hacky, need better way to do this.
func (m *mountStore) init(requeueWorkspace func(cluster logicalcluster.Path, workspace tenancyv1alpha1.Workspace) error,
	getMountObject func(ctx context.Context, cluster logicalcluster.Path, ref *v1.ObjectReference) (*unstructured.Unstructured, error),
) {
	m.initOnce.Do(func() {
		m.requeueWorkspace = requeueWorkspace
		m.getMountObject = getMountObject
		m.initDone = true
	})
}

func key(cluster logicalcluster.Path, ref v1.ObjectReference) string {
	return cluster.String() + "/" + ref.Kind + "/" + ref.APIVersion + "/" + ref.Namespace + "/" + ref.Name
}

func (m *mountStore) run(ctx context.Context) {
	interval := time.NewTicker(tickerInterval)
	defer interval.Stop()
	for {
		if !m.initDone {
			// wait for init
			time.Sleep(time.Second)
			continue
		}
		select {
		case <-interval.C:
			for key := range m.mountTrackers {
				m.requeueIfChanged(ctx, key)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (m *mountStore) requeueIfChanged(ctx context.Context, key string) {
	m.mountTrackerLock.Lock()
	current := m.mountTrackers[key]
	m.mountTrackerLock.Unlock()

	if current.Empty() {
		return
	}

	obj, err := m.getMountObject(ctx, current.cluster, current.ref)
	if err != nil {
		if apierrors.IsNotFound(err) {
			m.remove(key)
			return
		}
	}
	status, ok := obj.Object["status"].(map[string]interface{})
	if !ok {
		return
	}
	statusURL, ok := status["URL"].(string)
	if !ok {
		return
	}
	statusPhase, ok := status["phase"].(string)
	if !ok {
		return
	}

	if statusURL != current.lastSeenURL || statusPhase != string(current.lastSeenPhase) {
		current.lastSeenURL = statusURL
		current.lastSeenPhase = tenancyv1alpha1.MountPhaseType(statusPhase)
		err := m.requeueWorkspace(current.cluster, *current.workspace)
		if err != nil {
			return
		}
		m.updateEntry(current)
	}
}

func (m *mountStore) add(cluster logicalcluster.Path, ref *v1.ObjectReference, workspace *tenancyv1alpha1.Workspace) {
	m.mountTrackerLock.Lock()
	defer m.mountTrackerLock.Unlock()
	key := key(cluster, *ref)
	v := m.mountTrackers[key]
	if v.Empty() {
		m.mountTrackers[key] = mountStoreEntry{
			cluster:   cluster,
			ref:       ref,
			workspace: workspace,
		}
	}
}

func (m *mountStore) remove(key string) {
	m.mountTrackerLock.Lock()
	defer m.mountTrackerLock.Unlock()
	delete(m.mountTrackers, key)
}

func (m *mountStore) updateEntry(entry mountStoreEntry) {
	m.mountTrackerLock.Lock()
	defer m.mountTrackerLock.Unlock()
	m.mountTrackers[key(entry.cluster, *entry.ref)] = entry
}
