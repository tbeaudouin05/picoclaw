package cron

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestSaveStore_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")

	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob("test", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "hello", "cli", "direct", 0)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("cron store has permission %04o, want 0600", perm)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func setupService(handler JobHandler) (*CronService, string) {
	tmpFile := fmt.Sprintf("test_cron_%d.json", time.Now().UnixNano())
	cs := NewCronService(tmpFile, handler)
	return cs, tmpFile
}

func TestCronService_CRUD(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	// Test AddJob
	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "at", AtMS: &at}, "msg", "ch", "to", 0)
	if err != nil || job.ID == "" {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Test ListJobs
	if len(cs.ListJobs(true)) != 1 {
		t.Error("ListJobs should return 1 job")
	}

	// Test UpdateJob
	job.Name = "UpdatedName"
	err = cs.UpdateJob(job)
	if err != nil || cs.store.Jobs[0].Name != "UpdatedName" {
		t.Error("UpdateJob failed")
	}

	// Test EnableJob
	cs.EnableJob(job.ID, false)
	if cs.store.Jobs[0].Enabled != false || cs.store.Jobs[0].State.NextRunAtMS != nil {
		t.Error("EnableJob(false) failed to clear state")
	}

	// Test RemoveJob
	removed := cs.RemoveJob(job.ID)
	if !removed || len(cs.store.Jobs) != 0 {
		t.Error("RemoveJob failed")
	}
}

func TestCronService_GetJobReturnsCopy(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "ch", "to", 0)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected initial next run")
	}
	nextRun := *job.State.NextRunAtMS

	got, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("GetJob should find job")
	}
	got.Name = "mutated"
	got.Payload.Message = "changed"
	if got.Schedule.EveryMS != nil {
		*got.Schedule.EveryMS = 120_000
	}
	if got.State.NextRunAtMS != nil {
		*got.State.NextRunAtMS = time.Now().Add(3 * time.Hour).UnixMilli()
	}

	again, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("GetJob should still find job")
	}
	if again.Name != "Task1" || again.Payload.Message != "msg" {
		t.Fatalf("GetJob should return a copy, got %+v", again)
	}
	if again.Schedule.EveryMS == nil || *again.Schedule.EveryMS != everyMS {
		t.Fatalf("GetJob should not alias schedule pointers, got %+v", again.Schedule)
	}
	if again.State.NextRunAtMS == nil || *again.State.NextRunAtMS != nextRun {
		t.Fatalf("GetJob should not alias state pointers, got %+v", again.State)
	}
}

func TestCronService_UpdateJobRecomputesNextRunOnScheduleOrEnabledChange(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	at := time.Now().Add(time.Hour).UnixMilli()
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "at", AtMS: &at}, "msg", "ch", "to", 0)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected initial next run")
	}
	initialNextRun := *job.State.NextRunAtMS

	everyMS := int64(30_000)
	job.Schedule = CronSchedule{Kind: "every", EveryMS: &everyMS}
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob schedule failed: %v", err)
	}
	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("updated job not found")
	}
	if updated.State.NextRunAtMS == nil {
		t.Fatal("expected recomputed next run after schedule change")
	}
	if *updated.State.NextRunAtMS == initialNextRun {
		t.Fatalf("next run should be recomputed, still %d", initialNextRun)
	}

	if disabled := cs.EnableJob(job.ID, false); disabled == nil {
		t.Fatal("EnableJob(false) returned nil")
	}
	disabled, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("disabled job not found")
	}
	disabled.Enabled = true
	if err := cs.UpdateJob(disabled); err != nil {
		t.Fatalf("UpdateJob enabled failed: %v", err)
	}
	reenabled, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("reenabled job not found")
	}
	if !reenabled.Enabled || reenabled.State.NextRunAtMS == nil {
		t.Fatalf("expected enabled job with next run, got %+v", reenabled)
	}
}

func TestCronService_UpdateJobPreservesRunStateOnPayloadOnlyChange(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob("Task1", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "ch", "to", 0)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	lastRun := time.Now().Add(-time.Minute).UnixMilli()
	job.State.LastRunAtMS = &lastRun
	job.State.LastStatus = "ok"
	job.State.LastError = "previous"
	if job.State.NextRunAtMS == nil {
		t.Fatal("expected next run before update")
	}
	nextRun := *job.State.NextRunAtMS

	job.Payload.Message = "updated msg"
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob failed: %v", err)
	}

	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("updated job not found")
	}
	if updated.State.LastRunAtMS == nil || *updated.State.LastRunAtMS != lastRun {
		t.Fatalf("last run changed: %+v", updated.State)
	}
	if updated.State.LastStatus != "ok" || updated.State.LastError != "previous" {
		t.Fatalf("last status changed: %+v", updated.State)
	}
	if updated.State.NextRunAtMS == nil || *updated.State.NextRunAtMS != nextRun {
		t.Fatalf("next run should be preserved: before=%d after=%+v", nextRun, updated.State.NextRunAtMS)
	}
}

// 2. Test Cron Expression Calculation Logic
func TestCronService_ComputeNextRun(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	now := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli()

	tests := []struct {
		name     string
		schedule CronSchedule
		wantNil  bool
	}{
		{"Valid Cron", CronSchedule{Kind: "cron", Expr: "0 * * * *"}, false},
		{"Invalid Cron", CronSchedule{Kind: "cron", Expr: "invalid"}, true},
		{"Every MS", CronSchedule{Kind: "every", EveryMS: int64Ptr(5000)}, false},
		{"At Future", CronSchedule{Kind: "at", AtMS: int64Ptr(now + 1000)}, false},
		{"At Past", CronSchedule{Kind: "at", AtMS: int64Ptr(now - 1000)}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cs.computeNextRun(&tt.schedule, now)
			if (got == nil) != tt.wantNil {
				t.Errorf("%s: got %v, wantNil %v", tt.name, got, tt.wantNil)
			}
		})
	}
}

// 3. Test Execution Flow
func TestCronService_ExecutionFlow(t *testing.T) {
	var mu sync.Mutex
	executedJobs := make(map[string]bool)

	handler := func(job *CronJob) (string, error) {
		mu.Lock()
		executedJobs[job.ID] = true
		mu.Unlock()
		return "ok", nil
	}

	cs, path := setupService(handler)
	defer os.Remove(path)

	// Start the service
	if err := cs.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer cs.Stop()

	// Add a job then runs 100ms from now
	target := time.Now().Add(100 * time.Millisecond).UnixMilli()
	job, _ := cs.AddJob("FastJob", CronSchedule{Kind: "at", AtMS: &target}, "", "", "", 0)

	// Check for job execution with a timeout
	success := false
	for range 20 {
		mu.Lock()
		if executedJobs[job.ID] {
			success = true
			mu.Unlock()
			break
		}
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}

	if !success {
		t.Error("Job was not executed in time")
	}

	// check that the job is removed after execution (DeleteAfterRun = true)
	status := cs.Status()
	if status["jobs"].(int) != 0 {
		t.Errorf("Job should be deleted after run, got count: %v", status["jobs"])
	}
}

func TestCronService_PersistenceIntegrity(t *testing.T) {
	tmpFile := "persist_test.json"
	defer os.Remove(tmpFile)

	// write a job and persist
	cs1 := NewCronService(tmpFile, nil)
	at := int64(2000000000000)
	cs1.AddJob("PersistMe", CronSchedule{Kind: "at", AtMS: &at}, "payload", "ch1", "", 0)

	// check file exists
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Fatal("Store file was not created")
	}

	// reload and check data integrity
	cs2 := NewCronService(tmpFile, nil)
	if err := cs2.Load(); err != nil {
		t.Fatalf("Failed to load store: %v", err)
	}

	jobs := cs2.ListJobs(true)
	if len(jobs) != 1 || jobs[0].Name != "PersistMe" {
		t.Errorf("Data corruption after reload. Got: %+v", jobs)
	}

	// test loading invalid JSON
	os.WriteFile(tmpFile, []byte("{invalid json}"), 0o644)
	cs3 := NewCronService(tmpFile, nil)
	err := cs3.loadStore()
	if err == nil {
		t.Error("Should return error when loading invalid JSON")
	}
}

// TestNotify_BuffersPendingWake proves the lost-wakeup race is fixed: notify()
// must buffer a pending wake signal even when no runLoop goroutine is parked on
// wakeChan. With an unbuffered channel the default branch drops the send and
// the loop sleeps through the next due job.
func TestNotify_BuffersPendingWake(t *testing.T) {
	// Use the real constructor so that a regression to make(chan struct{}) is caught.
	cs, path := setupService(nil)
	defer os.Remove(path)

	// No runLoop goroutine is started — simulate it being busy / not yet parked.
	cs.notify()

	if len(cs.wakeChan) != 1 {
		t.Fatal("notify() dropped the wake signal: channel is empty (lost-wakeup bug)")
	}

	// A second call must not block and must leave channel length at 1 (already pending).
	cs.notify()
	if len(cs.wakeChan) != 1 {
		t.Fatalf("expected channel length 1 after second notify(), got %d", len(cs.wakeChan))
	}
}

// TestCronService_SetMaxConcurrentJobs_DefaultNil verifies that a freshly
// constructed service has no semaphore (the config layer, not the constructor,
// sets the effective default of 1 via SetMaxConcurrentJobs).
func TestCronService_SetMaxConcurrentJobs_DefaultNil(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	if cs.jobSem != nil {
		t.Error("expected jobSem to be nil before SetMaxConcurrentJobs is called")
	}
}

// TestCronService_SetMaxConcurrentJobs_Semaphore verifies that SetMaxConcurrentJobs
// creates a semaphore of the requested capacity and that <= 0 clamps to 1.
func TestCronService_SetMaxConcurrentJobs_Semaphore(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	cs.SetMaxConcurrentJobs(3)
	if cap(cs.jobSem) != 3 {
		t.Errorf("jobSem cap = %d, want 3", cap(cs.jobSem))
	}

	cs.SetMaxConcurrentJobs(0)
	if cap(cs.jobSem) != 1 {
		t.Errorf("jobSem cap = %d, want 1 after SetMaxConcurrentJobs(0)", cap(cs.jobSem))
	}

	cs.SetMaxConcurrentJobs(-1)
	if cap(cs.jobSem) != 1 {
		t.Errorf("jobSem cap = %d, want 1 after SetMaxConcurrentJobs(-1)", cap(cs.jobSem))
	}
}

// TestCronService_BoundedConcurrency confirms that when N jobs are due at the
// same time, no more than the configured cap run simultaneously.
func TestCronService_BoundedConcurrency(t *testing.T) {
	const numJobs = 6
	const maxConcurrent = 2

	var mu sync.Mutex
	var currentRunning, peakRunning int
	completed := make(chan struct{}, numJobs)

	handler := func(job *CronJob) (string, error) {
		mu.Lock()
		currentRunning++
		if currentRunning > peakRunning {
			peakRunning = currentRunning
		}
		mu.Unlock()

		time.Sleep(150 * time.Millisecond) // slow enough to expose overlap

		mu.Lock()
		currentRunning--
		mu.Unlock()

		completed <- struct{}{}
		return "ok", nil
	}

	cs, path := setupService(handler)
	defer os.Remove(path)
	cs.SetMaxConcurrentJobs(maxConcurrent)

	if err := cs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cs.Stop()

	// Add all jobs with a common due time 100ms away so they all land in the
	// same checkJobs pass. Using Now() directly would return nil from
	// computeNextRun (which requires AtMS > now).
	atMS := time.Now().Add(100 * time.Millisecond).UnixMilli()
	for i := range numJobs {
		_, err := cs.AddJob(fmt.Sprintf("bounded-%d", i), CronSchedule{Kind: "at", AtMS: &atMS}, "", "cli", "direct", 0)
		if err != nil {
			t.Fatalf("AddJob: %v", err)
		}
	}

	// Wait for every job to complete.
	timeout := time.After(10 * time.Second)
	for range numJobs {
		select {
		case <-completed:
		case <-timeout:
			t.Fatal("timed out waiting for all jobs to complete")
		}
	}

	if peakRunning > maxConcurrent {
		t.Errorf("peak concurrent executions = %d, exceeds cap %d", peakRunning, maxConcurrent)
	}
	if peakRunning == 0 {
		t.Error("no jobs ran at all")
	}
}

// TestCronService_SameJobNoOverlap verifies that a single job instance is
// skipped when it is still in-flight (runningJobs protection).
func TestCronService_SameJobNoOverlap(t *testing.T) {
	handlerCalled := 0
	handler := func(job *CronJob) (string, error) {
		handlerCalled++
		return "ok", nil
	}

	cs, path := setupService(handler)
	defer os.Remove(path)

	// Add an every-100ms job.
	everyMS := int64(100)
	job, err := cs.AddJob("overlap-job", CronSchedule{Kind: "every", EveryMS: &everyMS}, "", "cli", "direct", 0)
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Make it appear due right now.
	cs.mu.Lock()
	pastMS := time.Now().UnixMilli() - 1
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &pastMS
		}
	}
	// Mark service as running so checkJobs does not return early.
	cs.running = true
	cs.mu.Unlock()
	defer func() {
		cs.mu.Lock()
		cs.running = false
		cs.mu.Unlock()
	}()

	// Simulate the job already running by putting it in runningJobs.
	cs.runMu.Lock()
	cs.runningJobs[job.ID] = struct{}{}
	cs.runMu.Unlock()

	// checkJobs should see the job as due but skip it.
	cs.checkJobs()
	time.Sleep(50 * time.Millisecond)

	cs.runMu.Lock()
	delete(cs.runningJobs, job.ID)
	cs.runMu.Unlock()

	if handlerCalled != 0 {
		t.Errorf("handler called %d time(s) while job was in runningJobs; want 0", handlerCalled)
	}
}

// TestCronService_OverlapSkip_NextRunPreserved is a regression test: a job that
// is overlap-skipped (same job still in-flight) must retain a valid NextRunAtMS
// so the scheduler does not permanently lose its schedule.
func TestCronService_OverlapSkip_NextRunPreserved(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	everyMS := int64(1000)
	job, err := cs.AddJob("skip-me", CronSchedule{Kind: "every", EveryMS: &everyMS}, "", "cli", "direct", 0)
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	// Make the job appear due and mark service as running so checkJobs proceeds.
	cs.mu.Lock()
	pastMS := time.Now().UnixMilli() - 1
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &pastMS
		}
	}
	cs.running = true
	cs.mu.Unlock()
	defer func() {
		cs.mu.Lock()
		cs.running = false
		cs.mu.Unlock()
	}()

	// Simulate the job already executing.
	cs.runMu.Lock()
	cs.runningJobs[job.ID] = struct{}{}
	cs.runMu.Unlock()

	before := time.Now().UnixMilli()
	cs.checkJobs()
	after := time.Now().UnixMilli()

	cs.runMu.Lock()
	delete(cs.runningJobs, job.ID)
	cs.runMu.Unlock()

	cs.mu.RLock()
	var nextRunAtMS *int64
	for _, j := range cs.store.Jobs {
		if j.ID == job.ID {
			nextRunAtMS = j.State.NextRunAtMS
			break
		}
	}
	cs.mu.RUnlock()

	if nextRunAtMS == nil {
		t.Fatal("NextRunAtMS is nil after overlap skip — scheduling state was lost")
	}
	// Next run must be in the future, computed from roughly the time checkJobs ran.
	expectedMin := before + everyMS
	expectedMax := after + everyMS + 200 // fuzz for clock jitter
	if *nextRunAtMS < expectedMin || *nextRunAtMS > expectedMax {
		t.Errorf("NextRunAtMS = %d, want in [%d, %d]", *nextRunAtMS, expectedMin, expectedMax)
	}
}

// TestCronService_DefaultCap1_Serialization confirms that SetMaxConcurrentJobs(1)
// forces all jobs to run serially: peak concurrency must never exceed 1.
func TestCronService_DefaultCap1_Serialization(t *testing.T) {
	const numJobs = 4

	var mu sync.Mutex
	var currentRunning, peakRunning int
	completed := make(chan struct{}, numJobs)

	handler := func(job *CronJob) (string, error) {
		mu.Lock()
		currentRunning++
		if currentRunning > peakRunning {
			peakRunning = currentRunning
		}
		mu.Unlock()

		time.Sleep(100 * time.Millisecond)

		mu.Lock()
		currentRunning--
		mu.Unlock()

		completed <- struct{}{}
		return "ok", nil
	}

	cs, path := setupService(handler)
	defer os.Remove(path)
	cs.SetMaxConcurrentJobs(1)

	if err := cs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cs.Stop()

	atMS := time.Now().Add(100 * time.Millisecond).UnixMilli()
	for i := range numJobs {
		_, err := cs.AddJob(fmt.Sprintf("serial-%d", i), CronSchedule{Kind: "at", AtMS: &atMS}, "", "cli", "direct", 0)
		if err != nil {
			t.Fatalf("AddJob: %v", err)
		}
	}

	timeout := time.After(10 * time.Second)
	for range numJobs {
		select {
		case <-completed:
		case <-timeout:
			t.Fatal("timed out waiting for jobs to complete")
		}
	}

	if peakRunning > 1 {
		t.Errorf("peak concurrent executions = %d with cap=1, want ≤1", peakRunning)
	}
	if peakRunning == 0 {
		t.Error("no jobs ran at all")
	}
}

func TestCronService_ConcurrentAccess(t *testing.T) {
	cs, path := setupService(nil)
	defer os.Remove(path)

	cs.Start()
	defer cs.Stop()

	var wg sync.WaitGroup
	workers := 10
	iterations := 50

	wg.Add(workers * 2)

	// add jobs concurrently
	for i := range workers {
		go func(id int) {
			defer wg.Done()
			for j := range iterations {
				at := time.Now().Add(time.Hour).UnixMilli()
				cs.AddJob(fmt.Sprintf("Job-%d-%d", id, j), CronSchedule{Kind: "at", AtMS: &at}, "", "", "", 0)
				time.Sleep(100 * time.Microsecond)
			}
		}(i)
	}

	// read and update jobs concurrently
	for range workers {
		go func() {
			defer wg.Done()
			for j := range iterations {
				jobs := cs.ListJobs(true)
				if len(jobs) > 0 {
					cs.EnableJob(jobs[0].ID, j%2 == 0)
				}
				time.Sleep(100 * time.Microsecond)
			}
		}()
	}

	wg.Wait()
}

func TestCronService_MaxRuns_DisablesAfterN(t *testing.T) {
	cs, path := setupService(func(job *CronJob) (string, error) { return "ok", nil })
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob("bounded", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "cli", "direct", 3)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	for range 3 {
		cs.executeJobByID(job.ID)
	}

	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if updated.Enabled {
		t.Fatal("expected job to be disabled after max runs")
	}
	if updated.State.RunCount != 3 {
		t.Fatalf("RunCount = %d, want 3", updated.State.RunCount)
	}
	if updated.State.NextRunAtMS != nil {
		t.Fatal("expected next run to be cleared after max runs")
	}
}

func TestCronService_MaxRuns_ZeroIsUnlimited(t *testing.T) {
	cs, path := setupService(func(job *CronJob) (string, error) { return "ok", nil })
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob("unlimited", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "cli", "direct", 0)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	for range 5 {
		cs.executeJobByID(job.ID)
	}

	updated, ok := cs.GetJob(job.ID)
	if !ok {
		t.Fatal("job not found")
	}
	if !updated.Enabled {
		t.Fatal("expected job to remain enabled with maxRuns=0")
	}
	if updated.State.RunCount != 5 {
		t.Fatalf("RunCount = %d, want 5", updated.State.RunCount)
	}
	if updated.State.NextRunAtMS == nil {
		t.Fatal("expected next run to remain scheduled")
	}
}

func TestCronService_MaxRuns_RunCountPersisted(t *testing.T) {
	cs, path := setupService(func(job *CronJob) (string, error) { return "ok", nil })
	defer os.Remove(path)

	everyMS := int64(60_000)
	job, err := cs.AddJob("persist-run-count", CronSchedule{Kind: "every", EveryMS: &everyMS}, "msg", "cli", "direct", 2)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	cs.executeJobByID(job.ID)

	reloaded := NewCronService(path, nil)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	got, ok := reloaded.GetJob(job.ID)
	if !ok {
		t.Fatal("reloaded job not found")
	}
	if got.State.RunCount != 1 {
		t.Fatalf("RunCount = %d, want 1", got.State.RunCount)
	}
}
