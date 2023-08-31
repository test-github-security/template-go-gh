// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package store

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// MemStore is an in-memory implementation of Store, for testing.
type MemStore struct {
	mu             sync.Mutex
	cveRecords     map[string]*CVERecord
	updateRecords  map[string]*CommitUpdateRecord
	dirHashes      map[string]string
	ghsaRecords    map[string]*GHSARecord
	modScanRecords []*ModuleScanRecord
}

// NewMemStore creates a new, empty MemStore.
func NewMemStore() *MemStore {
	m := &MemStore{}
	_ = m.Clear(context.Background())
	return m
}

// Clear removes all data from the MemStore.
func (ms *MemStore) Clear(context.Context) error {
	ms.cveRecords = map[string]*CVERecord{}
	ms.updateRecords = map[string]*CommitUpdateRecord{}
	ms.dirHashes = map[string]string{}
	ms.ghsaRecords = map[string]*GHSARecord{}
	ms.modScanRecords = nil
	return nil
}

// CVERecords return all the CVERecords of the store.
func (ms *MemStore) CVERecords() map[string]*CVERecord {
	return ms.cveRecords
}

// CreateCommitUpdateRecord implements Store.CreateCommitUpdateRecord.
func (ms *MemStore) CreateCommitUpdateRecord(ctx context.Context, r *CommitUpdateRecord) error {
	r.ID = fmt.Sprint(rand.Uint32())
	if ms.updateRecords[r.ID] != nil {
		panic("duplicate ID")
	}
	r.UpdatedAt = time.Now()
	return ms.SetCommitUpdateRecord(ctx, r)
}

// SetCommitUpdateRecord implements Store.SetCommitUpdateRecord.
func (ms *MemStore) SetCommitUpdateRecord(_ context.Context, r *CommitUpdateRecord) error {
	if r.ID == "" {
		return errors.New("SetCommitUpdateRecord: need ID")
	}
	c := *r
	c.UpdatedAt = time.Now()
	ms.updateRecords[c.ID] = &c
	return nil
}

// ListCommitUpdateRecords implements Store.ListCommitUpdateRecords.
func (ms *MemStore) ListCommitUpdateRecords(_ context.Context, limit int) ([]*CommitUpdateRecord, error) {
	var urs []*CommitUpdateRecord
	for _, ur := range ms.updateRecords {
		urs = append(urs, ur)
	}
	sort.Slice(urs, func(i, j int) bool {
		return urs[i].StartedAt.After(urs[j].StartedAt)
	})
	if limit > 0 && len(urs) > limit {
		urs = urs[:limit]
	}
	return urs, nil
}

// GetCVERecord implements store.GetCVERecord.
func (ms *MemStore) GetCVERecord(ctx context.Context, id string) (*CVERecord, error) {
	return ms.cveRecords[id], nil
}

// ListCVERecordsWithTriageState implements Store.ListCVERecordsWithTriageState.
func (ms *MemStore) ListCVERecordsWithTriageState(_ context.Context, ts TriageState) ([]*CVERecord, error) {
	var crs []*CVERecord
	for _, r := range ms.cveRecords {
		if r.TriageState == ts {
			crs = append(crs, r)
		}
	}
	sort.Slice(crs, func(i, j int) bool {
		return crs[i].ID < crs[j].ID
	})
	return crs, nil
}

// CreateModuleScanRecord implements Store.CreateModuleScanRecord.
func (ms *MemStore) CreateModuleScanRecord(_ context.Context, r *ModuleScanRecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	ms.modScanRecords = append(ms.modScanRecords, r)
	return nil
}

// GetModuleScanRecord implements store.GetModuleScanRecord.
func (ms *MemStore) GetModuleScanRecord(_ context.Context, path, version string, dbTime time.Time) (*ModuleScanRecord, error) {
	var m *ModuleScanRecord
	for _, r := range ms.modScanRecords {
		if r.Path == path && r.Version == version && r.DBTime.Equal(dbTime) {
			if m == nil || m.FinishedAt.Before(r.FinishedAt) {
				m = r
			}
		}
	}
	return m, nil
}

// ListModuleScanRecords implements Store.ListModuleScanRecords.
func (ms *MemStore) ListModuleScanRecords(ctx context.Context, limit int) ([]*ModuleScanRecord, error) {
	rs := make([]*ModuleScanRecord, len(ms.modScanRecords))
	copy(rs, ms.modScanRecords)
	sort.Slice(rs, func(i, j int) bool { return rs[i].FinishedAt.After(rs[j].FinishedAt) })
	if limit == 0 || limit >= len(rs) {
		return rs, nil
	}
	return rs[:limit], nil
}

// GetDirectoryHash implements Transaction.GetDirectoryHash.
func (ms *MemStore) GetDirectoryHash(_ context.Context, dir string) (string, error) {
	return ms.dirHashes[dir], nil
}

// SetDirectoryHash implements Transaction.SetDirectoryHash.
func (ms *MemStore) SetDirectoryHash(_ context.Context, dir, hash string) error {
	ms.dirHashes[dir] = hash
	return nil
}

// RunTransaction implements Store.RunTransaction.
// A transaction runs with a single lock on the entire DB.
func (ms *MemStore) RunTransaction(ctx context.Context, f func(context.Context, Transaction) error) error {
	tx := &memTransaction{ms}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return f(ctx, tx)
}

// memTransaction implements Store.Transaction.
type memTransaction struct {
	ms *MemStore
}

// CreateCVERecord implements Transaction.CreateCVERecord.
func (tx *memTransaction) CreateCVERecord(r *CVERecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	tx.ms.cveRecords[r.ID] = r
	return nil
}

// SetCVERecord implements Transaction.SetCVERecord.
func (tx *memTransaction) SetCVERecord(r *CVERecord) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if tx.ms.cveRecords[r.ID] == nil {
		return fmt.Errorf("CVERecord with ID %q not found", r.ID)
	}
	tx.ms.cveRecords[r.ID] = r
	return nil
}

// GetCVERecords implements Transaction.GetCVERecords.
func (tx *memTransaction) GetCVERecords(startID, endID string) ([]*CVERecord, error) {
	var crs []*CVERecord
	for id, r := range tx.ms.cveRecords {
		if id >= startID && id <= endID {
			c := *r
			crs = append(crs, &c)
		}
	}
	// Sort for testing.
	sort.Slice(crs, func(i, j int) bool {
		return crs[i].ID < crs[j].ID
	})
	return crs, nil
}

// CreateGHSARecord implements Transaction.CreateGHSARecord.
func (tx *memTransaction) CreateGHSARecord(r *GHSARecord) error {
	if _, ok := tx.ms.ghsaRecords[r.GHSA.ID]; ok {
		return fmt.Errorf("GHSARecord %s already exists", r.GHSA.ID)
	}
	tx.ms.ghsaRecords[r.GHSA.ID] = r
	return nil
}

// SetGHSARecord implements Transaction.SetGHSARecord.
func (tx *memTransaction) SetGHSARecord(r *GHSARecord) error {
	if _, ok := tx.ms.ghsaRecords[r.GHSA.ID]; !ok {
		return fmt.Errorf("GHSARecord %s does not exist", r.GHSA.ID)
	}
	tx.ms.ghsaRecords[r.GHSA.ID] = r
	return nil
}

// GetGHSARecord implements Transaction.GetGHSARecord.
func (tx *memTransaction) GetGHSARecord(id string) (*GHSARecord, error) {
	if r, ok := tx.ms.ghsaRecords[id]; ok {
		return r, nil
	}
	return nil, nil
}

// GetGHSARecords implements Transaction.GetGHSARecords.
func (tx *memTransaction) GetGHSARecords() ([]*GHSARecord, error) {
	var recs []*GHSARecord
	for _, r := range tx.ms.ghsaRecords {
		recs = append(recs, r)
	}
	return recs, nil
}
