package accounts

// SetCatalogForTest replaces the process-wide model catalog with the given
// models and returns a function restoring the previous contents. Intended
// for tests in other packages (e.g. internal/translate) that need specific
// per-model limits (MaxOutput, MinThinkingBudget) without a live fetch.
func SetCatalogForTest(models []AvailableModelInfo) (restore func()) {
	c := globalModelCatalog
	c.mu.Lock()
	prevModels, prevMap := c.models, c.modelMap
	m := make(map[string]AvailableModelInfo, len(models))
	for _, mi := range models {
		m[mi.ID] = mi
	}
	c.models, c.modelMap = models, m
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.models, c.modelMap = prevModels, prevMap
		c.mu.Unlock()
	}
}
