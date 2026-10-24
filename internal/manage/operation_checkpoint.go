package manage

// saveOperation retains the CLI recovery journal and, when supplied by a local
// worker, a separate exact-operation checkpoint. Either write failure stops Apply.
func saveOperation(e Engine, result ApplyResult) error {
	if err := saveRecentOperation(e.StateDir, result); err != nil {
		return err
	}
	if e.RecordOperation != nil {
		return e.RecordOperation(result)
	}
	return nil
}
