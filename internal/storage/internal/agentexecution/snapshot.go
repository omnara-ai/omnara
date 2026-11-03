package agentexecution

import "slices"

func copyPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneOpening(opening Opening) Opening {
	opening.InputIDs = slices.Clone(opening.InputIDs)
	return opening
}

func cloneModel(model ModelContext) ModelContext {
	model.Opening = cloneOpening(model.Opening)
	model.RetryAt = copyPointer(model.RetryAt)
	return model
}

func cloneExecutionSnapshot(snapshot ExecutionSnapshot) ExecutionSnapshot {
	snapshot.Head.LogicalReadyAt = copyPointer(snapshot.Head.LogicalReadyAt)
	snapshot.Selection.LogicalReadyAt = copyPointer(snapshot.Selection.LogicalReadyAt)
	snapshot.Selection.Model = copyPointer(snapshot.Selection.Model)
	snapshot.Selection.Tool = copyPointer(snapshot.Selection.Tool)
	if snapshot.Selection.Model != nil {
		snapshot.Selection.Model.Opening = cloneOpening(snapshot.Selection.Model.Opening)
	}
	v := &snapshot.View
	v.Turn = copyPointer(v.Turn)
	if v.Turn != nil {
		v.Turn.InitialOpening = cloneOpening(v.Turn.InitialOpening)
	}
	v.NormalContext = copyPointer(v.NormalContext)
	if v.NormalContext != nil {
		*v.NormalContext = cloneModel(*v.NormalContext)
	}
	v.CompactionContext = copyPointer(v.CompactionContext)
	if v.CompactionContext != nil {
		*v.CompactionContext = cloneModel(*v.CompactionContext)
	}
	v.ToolBatch = copyPointer(v.ToolBatch)
	if v.ToolBatch != nil {
		v.ToolBatch.Output.Context = cloneModel(v.ToolBatch.Output.Context)
		v.ToolBatch.Completion = copyPointer(v.ToolBatch.Completion)
	}
	v.OutputLimit = copyPointer(v.OutputLimit)
	if v.OutputLimit != nil {
		v.OutputLimit.Context = cloneModel(v.OutputLimit.Context)
	}
	v.Config = copyPointer(v.Config)
	if v.Config != nil {
		v.Config.Opening = cloneOpening(v.Config.Opening)
	}
	v.Checkpoint = copyPointer(v.Checkpoint)
	if v.Checkpoint != nil {
		v.Checkpoint.Opening = cloneOpening(v.Checkpoint.Opening)
	}
	return snapshot
}
