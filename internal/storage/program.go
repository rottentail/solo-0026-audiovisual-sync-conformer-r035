package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

func (r *Repository) programPath(id string) string {
	return filepath.Join(r.root, "programs", id+".json")
}

func (r *Repository) programBundleDir(id string) string {
	return filepath.Join(r.root, "programs", "assets", id)
}

func (r *Repository) GetProgram(id string) (domain.ConformProgram, error) {
	var record domain.ConformProgram
	if err := readJSON(r.programPath(id), &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.ConformProgram{}, domain.NewError(
				"conform_program_not_found",
				404,
				"conform program %s does not exist",
				id,
			)
		}
		return domain.ConformProgram{}, fmt.Errorf("read conform program: %w", err)
	}
	return record, nil
}

// CreateProgram persists a queued program. A repeated request with the same
// segment order and correction ceiling returns the existing record; a
// different payload under the same id conflicts.
func (r *Repository) CreateProgram(record domain.ConformProgram) (domain.ConformProgram, bool, error) {
	lock := r.locks.forKey("program:" + record.ID)
	lock.Lock()
	defer lock.Unlock()
	existing, err := r.GetProgram(record.ID)
	switch {
	case err == nil:
		if sameProgramRequest(existing, record) {
			return existing, false, nil
		}
		return domain.ConformProgram{}, false, domain.NewError(
			"conform_program_conflict",
			409,
			"conform program %s already exists with different segments or correction ceiling",
			record.ID,
		)
	case !domain.IsErrorCode(err, "conform_program_not_found"):
		return domain.ConformProgram{}, false, err
	}
	if err := r.writeProgram(record); err != nil {
		return domain.ConformProgram{}, false, err
	}
	return record, true, nil
}

func (r *Repository) UpdateProgram(record domain.ConformProgram) error {
	lock := r.locks.forKey("program:" + record.ID)
	lock.Lock()
	defer lock.Unlock()
	current, err := r.GetProgram(record.ID)
	if err != nil {
		return err
	}
	if !domain.CanAdvanceConform(current.State, record.State) {
		return domain.NewError(
			"conform_transition_invalid",
			409,
			"conform program cannot move from %s to %s",
			current.State,
			record.State,
		)
	}
	return r.writeProgram(record)
}

func (r *Repository) CommitProgram(
	record domain.ConformProgram,
	bundle []byte,
	receipt []byte,
) (domain.ConformProgram, error) {
	lock := r.locks.forKey("program:" + record.ID)
	lock.Lock()
	defer lock.Unlock()
	current, err := r.GetProgram(record.ID)
	if err != nil {
		return domain.ConformProgram{}, err
	}
	if !domain.CanAdvanceConform(current.State, domain.ConformStateCompleted) {
		return domain.ConformProgram{}, domain.NewError(
			"conform_transition_invalid",
			409,
			"conform program cannot complete from %s",
			current.State,
		)
	}
	tempDir, err := os.MkdirTemp(filepath.Join(r.root, "tmp"), "program-"+record.ID+"-*")
	if err != nil {
		return domain.ConformProgram{}, fmt.Errorf("create program staging directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	if err := writeFileAtomic(filepath.Join(tempDir, "bundle.zip"), bundle); err != nil {
		return domain.ConformProgram{}, err
	}
	if err := writeFileAtomic(filepath.Join(tempDir, "receipt.json"), receipt); err != nil {
		return domain.ConformProgram{}, err
	}
	if err := syncDirectory(tempDir); err != nil {
		return domain.ConformProgram{}, fmt.Errorf("sync program staging directory: %w", err)
	}
	finalDir := r.programBundleDir(record.ID)
	if err := os.Rename(tempDir, finalDir); err != nil {
		if errors.Is(err, os.ErrExist) {
			return domain.ConformProgram{}, domain.NewError(
				"conform_bundle_conflict",
				409,
				"program bundle %s already exists",
				record.ID,
			)
		}
		return domain.ConformProgram{}, fmt.Errorf("commit program bundle directory: %w", err)
	}
	record.State = domain.ConformStateCompleted
	record.UpdatedAt = time.Now().UTC()
	if err := r.writeProgram(record); err != nil {
		os.RemoveAll(finalDir)
		return domain.ConformProgram{}, err
	}
	return record, nil
}

func (r *Repository) ReadProgramBundle(id string) ([]byte, domain.ConformProgramReceipt, error) {
	record, err := r.GetProgram(id)
	if err != nil {
		return nil, domain.ConformProgramReceipt{}, err
	}
	if record.State != domain.ConformStateCompleted {
		return nil, domain.ConformProgramReceipt{}, domain.NewError(
			"conform_not_ready",
			409,
			"conform program %s is not completed",
			id,
		)
	}
	bundle, err := os.ReadFile(filepath.Join(r.programBundleDir(id), "bundle.zip"))
	if err != nil {
		return nil, domain.ConformProgramReceipt{}, fmt.Errorf("read program bundle: %w", err)
	}
	if len(bundle) != record.BundleBytes || shaOrEmpty(bundle) != record.BundleDigest {
		return nil, domain.ConformProgramReceipt{}, domain.NewError(
			"bundle_integrity_failed",
			500,
			"stored program bundle does not match its completed record",
		)
	}
	var receipt domain.ConformProgramReceipt
	if err := readJSON(filepath.Join(r.programBundleDir(id), "receipt.json"), &receipt); err != nil {
		return nil, domain.ConformProgramReceipt{}, fmt.Errorf("read program receipt: %w", err)
	}
	return bundle, receipt, nil
}

func (r *Repository) writeProgram(record domain.ConformProgram) error {
	return writeJSON(r.programPath(record.ID), record)
}

func sameProgramRequest(existing, candidate domain.ConformProgram) bool {
	if existing.MaxCorrectionMS != candidate.MaxCorrectionMS ||
		len(existing.SegmentRequests) != len(candidate.SegmentRequests) {
		return false
	}
	for index := range existing.SegmentRequests {
		left := existing.SegmentRequests[index]
		right := candidate.SegmentRequests[index]
		if left.AnalysisID != right.AnalysisID ||
			!sameIntPointer(left.Range.StartFrame, right.Range.StartFrame) ||
			!sameIntPointer(left.Range.EndFrame, right.Range.EndFrame) {
			return false
		}
	}
	return true
}

func sameIntPointer(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
