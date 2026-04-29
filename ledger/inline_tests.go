package ledger

// inline tests is a global list of functions that are run again any enw library. Must pass

type inlineTest func(lib *Library)

var _inlineTests = make([]inlineTest, 0)

// registerInlineTest must be called from init -> no need for mutex
func registerInlineTest(t inlineTest) {
	_inlineTests = append(_inlineTests, t)
}

func runInlineTests(lib *Library) {
	for _, t := range _inlineTests {
		t(lib)
	}
}
