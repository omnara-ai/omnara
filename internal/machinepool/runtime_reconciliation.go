package machinepool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/management"
)

const (
	defaultRuntimeCandidatePageSize   = 200
	defaultRuntimeConfirmationGrace   = 2 * time.Minute
	providerRuntimeConcurrency        = 8
	providerRuntimeFailureCooldown    = time.Minute
	providerRuntimeMaxFailureCooldown = 15 * time.Minute
)

type RuntimeReconciliationConfig struct {
	PageSize          int32
	ConfirmationGrace time.Duration
	InactivityGrace   time.Duration
	Concurrency       int
}

func defaultRuntimeReconciliationConfig() RuntimeReconciliationConfig {
	return RuntimeReconciliationConfig{
		PageSize:          defaultRuntimeCandidatePageSize,
		ConfirmationGrace: defaultRuntimeConfirmationGrace,
		InactivityGrace:   defaultRuntimeConfirmationGrace,
		Concurrency:       providerRuntimeConcurrency,
	}
}

type RuntimeReconciliationStats struct {
	Pages                 int
	Scopes                int
	ScopesSkipped         int
	Targets               int
	Observed              int
	Running               int
	Inactive              int
	Transitional          int
	Terminated            int
	Unknown               int
	ProviderErrors        int
	MarkersSet            int
	MarkersCleared        int
	WakeAttemptsCleared   int
	Confirmations         int
	DeletionClaims        int
	DeletionClaimsSkipped int
}

type runtimeReconciliationState struct {
	mu                  sync.Mutex
	cooldowns           map[executionstore.ProviderRuntimeScopeKey]time.Time
	confirmationCursors map[executionstore.ProviderRuntimeScopeKey]storage.ID
}

func newRuntimeReconciliationState() *runtimeReconciliationState {
	return &runtimeReconciliationState{
		cooldowns:           make(map[executionstore.ProviderRuntimeScopeKey]time.Time),
		confirmationCursors: make(map[executionstore.ProviderRuntimeScopeKey]storage.ID),
	}
}

func (s *runtimeReconciliationState) coolingDown(
	scopeKey executionstore.ProviderRuntimeScopeKey,
	now time.Time,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cooldowns[scopeKey].After(now)
}

func (s *runtimeReconciliationState) pruneExpired(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, until := range s.cooldowns {
		if !until.After(now) {
			delete(s.cooldowns, key)
		}
	}
}

func (s *runtimeReconciliationState) recordFailure(
	scopeKey executionstore.ProviderRuntimeScopeKey,
	now time.Time,
	err error,
) {
	delay := providerRuntimeFailureCooldown
	if retryAfter, ok := providers.RetryAfter(err); ok {
		delay = max(delay, min(retryAfter, providerRuntimeMaxFailureCooldown))
	}
	until := now.Add(delay)
	s.mu.Lock()
	if current := s.cooldowns[scopeKey]; until.After(current) {
		s.cooldowns[scopeKey] = until
	}
	s.mu.Unlock()
}

func (s *runtimeReconciliationState) pruneConfirmationCursors(
	active []executionstore.ProviderRuntimeScopeKey,
) {
	keep := make(map[executionstore.ProviderRuntimeScopeKey]struct{}, len(active))
	for _, scopeKey := range active {
		keep[scopeKey] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for scopeKey := range s.confirmationCursors {
		if _, exists := keep[scopeKey]; !exists {
			delete(s.confirmationCursors, scopeKey)
		}
	}
}

func (s *runtimeReconciliationState) confirmationStart(
	scopeKey executionstore.ProviderRuntimeScopeKey,
	candidates []executionstore.ProviderRuntimeCandidate,
) int {
	s.mu.Lock()
	cursor := s.confirmationCursors[scopeKey]
	s.mu.Unlock()
	if cursor == storage.NilID {
		return 0
	}
	for index, candidate := range candidates {
		if candidate.MachineID == cursor {
			return (index + 1) % len(candidates)
		}
	}
	return 0
}

func (s *runtimeReconciliationState) recordConfirmationCursor(
	scopeKey executionstore.ProviderRuntimeScopeKey,
	machineID storage.ID,
) {
	s.mu.Lock()
	s.confirmationCursors[scopeKey] = machineID
	s.mu.Unlock()
}

func (s *runtimeReconciliationState) clearConfirmationCursor(
	scopeKey executionstore.ProviderRuntimeScopeKey,
) {
	s.mu.Lock()
	delete(s.confirmationCursors, scopeKey)
	s.mu.Unlock()
}

func (s *RuntimeReconciliationStats) add(other RuntimeReconciliationStats) {
	s.Pages += other.Pages
	s.Scopes += other.Scopes
	s.ScopesSkipped += other.ScopesSkipped
	s.Targets += other.Targets
	s.Observed += other.Observed
	s.Running += other.Running
	s.Inactive += other.Inactive
	s.Transitional += other.Transitional
	s.Terminated += other.Terminated
	s.Unknown += other.Unknown
	s.ProviderErrors += other.ProviderErrors
	s.MarkersSet += other.MarkersSet
	s.MarkersCleared += other.MarkersCleared
	s.WakeAttemptsCleared += other.WakeAttemptsCleared
	s.Confirmations += other.Confirmations
	s.DeletionClaims += other.DeletionClaims
	s.DeletionClaimsSkipped += other.DeletionClaimsSkipped
}

func (m Manager) DiscoverProviderRuntimeMismatches(
	ctx context.Context,
	config RuntimeReconciliationConfig,
) (RuntimeReconciliationStats, error) {
	if m.runtimeReconciliationState == nil {
		return RuntimeReconciliationStats{}, errors.New("provider runtime reconciliation is not initialized")
	}
	m.runtimeReconciliationState.pruneExpired(time.Now())
	config = normalizeRuntimeReconciliationConfig(config)
	installationID, err := m.Identity.GetInstallationID(ctx)
	if err != nil {
		return RuntimeReconciliationStats{}, err
	}
	var stats RuntimeReconciliationStats
	scopeOrder := make([]executionstore.ProviderRuntimeScopeKey, 0)
	scopeCandidates := make(map[executionstore.ProviderRuntimeScopeKey][]executionstore.ProviderRuntimeCandidate)
	cursor := executionstore.ListProviderRuntimeCandidatesInput{Limit: config.PageSize}
	for {
		page, err := m.Execution.ListProviderRuntimeDiscoveryCandidates(ctx, cursor)
		if err != nil {
			return stats, err
		}
		stats.Pages++
		for _, candidate := range page {
			if _, exists := scopeCandidates[candidate.ScopeKey]; !exists {
				scopeOrder = append(scopeOrder, candidate.ScopeKey)
			}
			scopeCandidates[candidate.ScopeKey] = append(
				scopeCandidates[candidate.ScopeKey],
				candidate,
			)
			stats.Targets++
		}
		if len(page) == 0 {
			break
		}
		last := page[len(page)-1]
		cursor.AfterMachineID = last.MachineID
		if len(page) < int(config.PageSize) {
			break
		}
	}
	stats.Scopes = len(scopeOrder)
	var statsMu sync.Mutex
	exactTasksByScope := make([][]runtimeExactObservationTask, len(scopeOrder))
	_, reconcileErr := runBoundedReconcile(
		ctx,
		len(scopeOrder),
		config.Concurrency,
		func(ctx context.Context, index int) error {
			scopeStats, scopeExactTasks, err := m.discoverProviderRuntimeScope(
				ctx,
				installationID,
				scopeCandidates[scopeOrder[index]],
			)
			statsMu.Lock()
			stats.add(scopeStats)
			statsMu.Unlock()
			exactTasksByScope[index] = scopeExactTasks
			return err
		},
	)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if reconcileErr != nil {
			return stats, reconcileErr
		}
		return stats, ctxErr
	}
	exactTasks := interleaveExactRuntimeObservationTasks(exactTasksByScope)
	_, exactErr := runBoundedReconcile(
		ctx,
		len(exactTasks),
		config.Concurrency,
		func(ctx context.Context, index int) error {
			taskStats, err := m.reconcileExactRuntimeObservation(
				ctx,
				installationID,
				exactTasks[index],
			)
			statsMu.Lock()
			stats.add(taskStats)
			statsMu.Unlock()
			return err
		},
	)
	if reconcileErr != nil {
		return stats, reconcileErr
	}
	return stats, exactErr
}

func (m Manager) discoverProviderRuntimeScope(
	ctx context.Context,
	installationID storage.ID,
	candidates []executionstore.ProviderRuntimeCandidate,
) (RuntimeReconciliationStats, []runtimeExactObservationTask, error) {
	var stats RuntimeReconciliationStats
	scopeKey := candidates[0].ScopeKey
	if m.runtimeReconciliationState.coolingDown(scopeKey, time.Now()) {
		stats.ScopesSkipped++
		return stats, nil, nil
	}
	runtimeProvider, err := m.providerForRuntimeScope(ctx, candidates[0])
	if err != nil {
		if m.recordProviderRuntimeFailure(ctx, scopeKey, err) {
			stats.ProviderErrors++
		}
		return stats, nil, err
	}
	targets := make([]providers.RuntimeTarget, 0, len(candidates))
	for _, candidate := range candidates {
		targets = append(targets, runtimeTarget(installationID, candidate))
	}
	observations, err := runtimeProvider.ObserveRuntimeStates(ctx, targets)
	if err != nil {
		if m.recordProviderRuntimeFailure(ctx, scopeKey, err) {
			stats.ProviderErrors++
		}
		return stats, nil, err
	}
	valid := validRuntimeObservations(candidates, observations)
	exactFailed := new(atomic.Bool)
	exactTasks := make([]runtimeExactObservationTask, 0)
	for _, candidate := range candidates {
		observation, ok := valid[candidate.MachineID]
		if exactRuntimeObservationRequired(candidate, observation, ok) {
			exactTasks = append(exactTasks, runtimeExactObservationTask{
				scopeKey:        scopeKey,
				runtimeProvider: runtimeProvider,
				candidate:       candidate,
				providerFailed:  exactFailed,
			})
			continue
		}
		if !ok || observation.State == providers.RuntimeStateTerminated {
			continue
		}
		observationStats, _, err := m.reconcileDiscoveredRuntimeObservation(
			ctx,
			scopeKey,
			runtimeProvider,
			candidate,
			observation,
		)
		stats.add(observationStats)
		if err != nil {
			return stats, exactTasks, err
		}
	}
	return stats, exactTasks, nil
}

type runtimeExactObservationTask struct {
	scopeKey        executionstore.ProviderRuntimeScopeKey
	runtimeProvider providers.RuntimeProvider
	candidate       executionstore.ProviderRuntimeCandidate
	providerFailed  *atomic.Bool
}

func interleaveExactRuntimeObservationTasks(
	tasksByScope [][]runtimeExactObservationTask,
) []runtimeExactObservationTask {
	total := 0
	positions := make([]int, len(tasksByScope))
	active := make([]int, 0, len(tasksByScope))
	for scopeIndex, scopeTasks := range tasksByScope {
		total += len(scopeTasks)
		if len(scopeTasks) > 0 {
			active = append(active, scopeIndex)
		}
	}
	tasks := make([]runtimeExactObservationTask, 0, total)
	next := make([]int, 0, len(active))
	for len(active) > 0 {
		for _, scopeIndex := range active {
			position := positions[scopeIndex]
			tasks = append(tasks, tasksByScope[scopeIndex][position])
			positions[scopeIndex]++
			if positions[scopeIndex] < len(tasksByScope[scopeIndex]) {
				next = append(next, scopeIndex)
			}
		}
		active, next = next, active[:0]
	}
	return tasks
}

func (m Manager) reconcileExactRuntimeObservation(
	ctx context.Context,
	installationID storage.ID,
	task runtimeExactObservationTask,
) (RuntimeReconciliationStats, error) {
	var stats RuntimeReconciliationStats
	if task.providerFailed.Load() {
		return stats, nil
	}
	providerCtx, cancel := context.WithTimeout(ctx, providerInspectionTimeout)
	stats.Confirmations++
	observation, err := task.runtimeProvider.ObserveRuntimeState(
		providerCtx,
		runtimeTarget(installationID, task.candidate),
	)
	cancel()
	if err != nil {
		task.providerFailed.Store(true)
		if m.recordProviderRuntimeFailure(ctx, task.scopeKey, err) {
			stats.ProviderErrors++
		}
		return stats, err
	}
	if !observation.State.Valid() || !runtimeObservationMatches(task.candidate, observation) {
		return stats, nil
	}
	observationStats, providerFailed, err := m.reconcileDiscoveredRuntimeObservation(
		ctx,
		task.scopeKey,
		task.runtimeProvider,
		task.candidate,
		observation,
	)
	if providerFailed {
		task.providerFailed.Store(true)
	}
	stats.add(observationStats)
	return stats, err
}

func (m Manager) reconcileDiscoveredRuntimeObservation(
	ctx context.Context,
	scopeKey executionstore.ProviderRuntimeScopeKey,
	runtimeProvider providers.RuntimeProvider,
	candidate executionstore.ProviderRuntimeCandidate,
	observation providers.RuntimeObservation,
) (RuntimeReconciliationStats, bool, error) {
	var stats RuntimeReconciliationStats
	stats.recordObservation(observation.State)
	switch observation.State {
	case providers.RuntimeStateRunning:
		marked, err := m.Execution.MarkProviderRuntimeMismatch(ctx, candidate)
		if err != nil {
			return stats, false, err
		}
		if marked {
			stats.MarkersSet++
		}
	case providers.RuntimeStateInactive:
		result, err := m.Execution.ApplyProviderRuntimeInactiveObservation(ctx, candidate)
		if err != nil {
			return stats, false, err
		}
		if result.Applied && candidate.ProviderRuntimeMismatchSince != nil {
			stats.MarkersCleared++
		}
		if result.WakeAttemptCleared {
			stats.WakeAttemptsCleared++
		}
	case providers.RuntimeStateTerminated:
		retirementStats, providerFailed, err := m.retireTerminatedProviderRuntime(
			ctx,
			scopeKey,
			candidate,
			runtimeProvider,
		)
		stats.add(retirementStats)
		if err != nil {
			return stats, providerFailed, err
		}
	case providers.RuntimeStateTransitional, providers.RuntimeStateUnknown:
	}
	return stats, false, nil
}

func exactRuntimeObservationRequired(
	candidate executionstore.ProviderRuntimeCandidate,
	observation providers.RuntimeObservation,
	found bool,
) bool {
	if found && observation.State == providers.RuntimeStateTerminated {
		return candidate.WakeAttemptExpiresAt == nil || candidate.WakeAttemptExpired
	}
	return candidate.WakeAttemptExpired &&
		(!found || observation.State == providers.RuntimeStateTransitional ||
			observation.State == providers.RuntimeStateUnknown)
}

func (m Manager) ConfirmProviderRuntimeMismatches(
	ctx context.Context,
	config RuntimeReconciliationConfig,
) (RuntimeReconciliationStats, error) {
	if m.runtimeReconciliationState == nil {
		return RuntimeReconciliationStats{}, errors.New("provider runtime reconciliation is not initialized")
	}
	m.runtimeReconciliationState.pruneExpired(time.Now())
	config = normalizeRuntimeReconciliationConfig(config)
	installationID, err := m.Identity.GetInstallationID(ctx)
	if err != nil {
		return RuntimeReconciliationStats{}, err
	}
	var stats RuntimeReconciliationStats
	scopeOrder := make([]executionstore.ProviderRuntimeScopeKey, 0)
	scopeCandidates := make(map[executionstore.ProviderRuntimeScopeKey][]executionstore.ProviderRuntimeCandidate)
	cursor := executionstore.ListDueProviderRuntimeMismatchesInput{
		Limit:             config.PageSize,
		ConfirmationGrace: config.ConfirmationGrace,
		InactivityGrace:   config.InactivityGrace,
	}

	for {
		page, err := m.Execution.ListDueProviderRuntimeMismatches(ctx, cursor)
		if err != nil {
			return stats, err
		}
		stats.Pages++
		for _, candidate := range page {
			if _, exists := scopeCandidates[candidate.ScopeKey]; !exists {
				scopeOrder = append(scopeOrder, candidate.ScopeKey)
			}
			scopeCandidates[candidate.ScopeKey] = append(
				scopeCandidates[candidate.ScopeKey],
				candidate,
			)
			stats.Targets++
		}
		if len(page) == 0 {
			break
		}
		last := page[len(page)-1]
		if last.ProviderRuntimeMismatchSince == nil {
			return stats, errors.New("due provider runtime candidate is missing its mismatch timestamp")
		}
		cursor.After = executionstore.ProviderRuntimeMismatchCursor{
			MismatchSince: *last.ProviderRuntimeMismatchSince,
			MachineID:     last.MachineID,
		}
		if len(page) < int(config.PageSize) {
			break
		}
	}
	stats.Scopes = len(scopeOrder)
	m.runtimeReconciliationState.pruneConfirmationCursors(scopeOrder)

	prepared := make([]*runtimeConfirmationScope, len(scopeOrder))
	var statsMu sync.Mutex
	_, prepareErr := runBoundedReconcile(
		ctx,
		len(scopeOrder),
		config.Concurrency,
		func(ctx context.Context, index int) error {
			scopeKey := scopeOrder[index]
			candidates := scopeCandidates[scopeKey]
			if m.runtimeReconciliationState.coolingDown(scopeKey, time.Now()) {
				statsMu.Lock()
				stats.ScopesSkipped++
				statsMu.Unlock()
				return nil
			}
			runtimeProvider, err := m.providerForRuntimeScope(ctx, candidates[0])
			if err != nil {
				if m.recordProviderRuntimeFailure(ctx, scopeKey, err) {
					statsMu.Lock()
					stats.ProviderErrors++
					statsMu.Unlock()
				}
				return err
			}
			prepared[index] = &runtimeConfirmationScope{
				scopeKey:        scopeKey,
				runtimeProvider: runtimeProvider,
				candidates:      candidates,
				start: m.runtimeReconciliationState.confirmationStart(
					scopeKey,
					candidates,
				),
			}
			return nil
		},
	)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if prepareErr != nil {
			return stats, prepareErr
		}
		return stats, ctxErr
	}

	active := make([]*runtimeConfirmationScope, 0, len(prepared))
	for _, scope := range prepared {
		if scope != nil {
			active = append(active, scope)
		}
	}
	firstErr := prepareErr
	for len(active) > 0 {
		tasks := buildRuntimeConfirmationStripe(active, config.Concurrency)
		if len(tasks) == 0 {
			break
		}
		_, stripeErr := runBoundedReconcile(
			ctx,
			len(tasks),
			config.Concurrency,
			func(ctx context.Context, index int) error {
				task := tasks[index]
				scopeStats, providerFailed, err := m.confirmProviderRuntimeCandidate(
					ctx,
					installationID,
					config,
					task.scope,
					task.candidateIndex,
				)
				if providerFailed {
					task.scope.providerFailed.Store(true)
					m.runtimeReconciliationState.recordConfirmationCursor(
						task.scope.scopeKey,
						task.scope.candidates[task.candidateIndex].MachineID,
					)
				}
				statsMu.Lock()
				stats.add(scopeStats)
				statsMu.Unlock()
				return err
			},
		)
		if firstErr == nil {
			firstErr = stripeErr
		}
		if ctx.Err() != nil {
			if firstErr == nil {
				firstErr = ctx.Err()
			}
			return stats, firstErr
		}

		next := active[:0]
		for _, scope := range active {
			if scope.providerFailed.Load() {
				continue
			}
			if scope.next < len(scope.candidates) {
				next = append(next, scope)
				continue
			}
			m.runtimeReconciliationState.clearConfirmationCursor(scope.scopeKey)
		}
		active = next
	}
	return stats, firstErr
}

type runtimeConfirmationScope struct {
	scopeKey        executionstore.ProviderRuntimeScopeKey
	runtimeProvider providers.RuntimeProvider
	candidates      []executionstore.ProviderRuntimeCandidate
	start           int
	next            int
	providerFailed  atomic.Bool
}

type runtimeConfirmationTask struct {
	scope          *runtimeConfirmationScope
	candidateIndex int
}

func buildRuntimeConfirmationStripe(
	scopes []*runtimeConfirmationScope,
	concurrency int,
) []runtimeConfirmationTask {
	if len(scopes) == 0 || concurrency <= 0 {
		return nil
	}
	targetCount := max(concurrency, len(scopes))
	tasks := make([]runtimeConfirmationTask, 0, targetCount)
	scheduled := make([]int, len(scopes))
	for len(tasks) < targetCount {
		progressed := false
		for index, scope := range scopes {
			offset := scope.next + scheduled[index]
			if offset >= len(scope.candidates) {
				continue
			}
			candidateIndex := (scope.start + offset) % len(scope.candidates)
			tasks = append(tasks, runtimeConfirmationTask{
				scope:          scope,
				candidateIndex: candidateIndex,
			})
			scheduled[index]++
			progressed = true
			if len(tasks) == targetCount {
				break
			}
		}
		if !progressed {
			break
		}
	}
	for index, count := range scheduled {
		scopes[index].next += count
	}
	return tasks
}

func (m Manager) confirmProviderRuntimeCandidate(
	ctx context.Context,
	installationID storage.ID,
	config RuntimeReconciliationConfig,
	scope *runtimeConfirmationScope,
	candidateIndex int,
) (RuntimeReconciliationStats, bool, error) {
	var stats RuntimeReconciliationStats
	candidate := scope.candidates[candidateIndex]
	providerCtx, cancel := context.WithTimeout(ctx, providerInspectionTimeout)
	stats.Confirmations++
	observation, err := scope.runtimeProvider.ObserveRuntimeState(
		providerCtx,
		runtimeTarget(installationID, candidate),
	)
	cancel()
	if err != nil {
		providerFailed := m.recordProviderRuntimeFailure(ctx, scope.scopeKey, err)
		if providerFailed {
			stats.ProviderErrors++
		}
		return stats, providerFailed, err
	}
	if !observation.State.Valid() || !runtimeObservationMatches(candidate, observation) {
		return stats, false, nil
	}
	stats.recordObservation(observation.State)
	switch observation.State {
	case providers.RuntimeStateInactive:
		result, err := m.Execution.ApplyProviderRuntimeInactiveObservation(ctx, candidate)
		if err != nil {
			return stats, false, err
		}
		if result.Applied && candidate.ProviderRuntimeMismatchSince != nil {
			stats.MarkersCleared++
		}
		if result.WakeAttemptCleared {
			stats.WakeAttemptsCleared++
		}
		return stats, false, nil
	case providers.RuntimeStateTerminated:
		retirementStats, providerFailed, err := m.retireTerminatedProviderRuntime(
			ctx,
			scope.scopeKey,
			candidate,
			scope.runtimeProvider,
		)
		stats.add(retirementStats)
		if err != nil {
			return stats, providerFailed, err
		}
		return stats, providerFailed, nil
	case providers.RuntimeStateRunning:
		claim, claimed, err := m.Execution.ClaimProviderRuntimeMismatchDeletion(
			ctx,
			executionstore.ClaimProviderRuntimeMismatchDeletionInput{
				Candidate:         candidate,
				ConfirmationGrace: config.ConfirmationGrace,
				InactivityGrace:   config.InactivityGrace,
			},
		)
		if err != nil {
			return stats, false, err
		}
		if !claimed {
			stats.DeletionClaimsSkipped++
			return stats, false, nil
		}
		stats.DeletionClaims++
		providerFailed, err := m.deleteClaimedMachine(ctx, claim, scope.runtimeProvider)
		if err != nil {
			providerFailed = providerFailed && m.recordProviderRuntimeFailure(ctx, scope.scopeKey, err)
			if providerFailed {
				stats.ProviderErrors++
			}
			return stats, providerFailed, err
		}
	case providers.RuntimeStateTransitional, providers.RuntimeStateUnknown:
		return stats, false, nil
	}
	return stats, false, nil
}

func (m Manager) retireTerminatedProviderRuntime(
	ctx context.Context,
	scopeKey executionstore.ProviderRuntimeScopeKey,
	candidate executionstore.ProviderRuntimeCandidate,
	deleter providers.MachineDeleter,
) (RuntimeReconciliationStats, bool, error) {
	var stats RuntimeReconciliationStats
	claim, claimed, err := m.Execution.ClaimProviderRuntimeTerminatedDeletion(ctx, candidate)
	if err != nil || !claimed {
		if err == nil {
			stats.DeletionClaimsSkipped++
		}
		return stats, false, err
	}
	stats.DeletionClaims++
	providerFailed, err := m.deleteClaimedMachine(ctx, claim, deleter)
	if providerFailed {
		providerFailed = m.recordProviderRuntimeFailure(ctx, scopeKey, err)
		if providerFailed {
			stats.ProviderErrors++
		}
	}
	return stats, providerFailed, err
}

func (m Manager) providerForRuntimeScope(
	ctx context.Context,
	candidate executionstore.ProviderRuntimeCandidate,
) (providers.RuntimeProvider, error) {
	definition, ok := m.Catalog.definition(candidate.Provider)
	if !ok {
		return nil, fmt.Errorf("machine provider %q is not configured", candidate.Provider)
	}
	runtimeDefinition, ok := definition.(providers.RuntimeProviderDefinition)
	if !ok {
		return nil, fmt.Errorf("machine provider %q does not support runtime protection", candidate.Provider)
	}
	credential, err := m.Execution.ResolveMachineProviderCredential(
		ctx,
		candidate.OrgID,
		candidate.ManagementKind,
		candidate.ProviderAuthSecretID,
		candidate.ProviderAuthEnvVar,
	)
	if err != nil {
		return nil, err
	}
	if candidate.ManagementKind == management.Tenant &&
		credential.VersionID != candidate.ProviderAuthVersionID {
		return nil, errors.New("machine provider credential changed during runtime reconciliation")
	}
	runtimeProvider, err := runtimeDefinition.NewRuntimeProvider(candidate.ProviderConfig, providers.RuntimeConfig{
		Omnara:            m.Omnara,
		ProviderAuthToken: credential.Token,
	})
	if err != nil {
		return nil, err
	}
	return runtimeProvider, nil
}

func (m Manager) recordProviderRuntimeFailure(
	ctx context.Context,
	scopeKey executionstore.ProviderRuntimeScopeKey,
	err error,
) bool {
	if ctx.Err() != nil {
		return false
	}
	m.runtimeReconciliationState.recordFailure(scopeKey, time.Now(), err)
	return true
}

func runtimeTarget(
	installationID storage.ID,
	candidate executionstore.ProviderRuntimeCandidate,
) providers.RuntimeTarget {
	return providers.RuntimeTarget{
		InstallationID:      installationID,
		MachineID:           candidate.MachineID,
		ProviderResourceID:  candidate.ProviderResourceID,
		MachineProvisioning: candidate.MachineProvisioning,
	}
}

func validRuntimeObservations(
	candidates []executionstore.ProviderRuntimeCandidate,
	observations []providers.RuntimeObservation,
) map[storage.ID]providers.RuntimeObservation {
	requested := make(map[storage.ID]executionstore.ProviderRuntimeCandidate, len(candidates))
	for _, candidate := range candidates {
		requested[candidate.MachineID] = candidate
	}
	valid := make(map[storage.ID]providers.RuntimeObservation, len(observations))
	duplicates := make(map[storage.ID]struct{})
	for _, observation := range observations {
		candidate, ok := requested[observation.MachineID]
		if !ok || !observation.State.Valid() ||
			!runtimeObservationMatches(candidate, observation) {
			continue
		}
		if _, exists := valid[observation.MachineID]; exists {
			delete(valid, observation.MachineID)
			duplicates[observation.MachineID] = struct{}{}
			continue
		}
		if _, duplicate := duplicates[observation.MachineID]; duplicate {
			continue
		}
		valid[observation.MachineID] = observation
	}
	return valid
}

func runtimeObservationMatches(
	candidate executionstore.ProviderRuntimeCandidate,
	observation providers.RuntimeObservation,
) bool {
	return observation.MachineID == candidate.MachineID &&
		observation.ProviderResourceID == candidate.ProviderResourceID
}

func normalizeRuntimeReconciliationConfig(
	config RuntimeReconciliationConfig,
) RuntimeReconciliationConfig {
	defaults := defaultRuntimeReconciliationConfig()
	if config.PageSize <= 0 {
		config.PageSize = defaults.PageSize
	}
	if config.ConfirmationGrace <= 0 {
		config.ConfirmationGrace = defaults.ConfirmationGrace
	}
	if config.InactivityGrace <= 0 {
		config.InactivityGrace = defaults.InactivityGrace
	}
	if config.Concurrency <= 0 {
		config.Concurrency = defaults.Concurrency
	}
	return config
}

func (s *RuntimeReconciliationStats) recordObservation(state providers.RuntimeState) {
	s.Observed++
	switch state {
	case providers.RuntimeStateRunning:
		s.Running++
	case providers.RuntimeStateInactive:
		s.Inactive++
	case providers.RuntimeStateTransitional:
		s.Transitional++
	case providers.RuntimeStateTerminated:
		s.Terminated++
	case providers.RuntimeStateUnknown:
		s.Unknown++
	}
}
