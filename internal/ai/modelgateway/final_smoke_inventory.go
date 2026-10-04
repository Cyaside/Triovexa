package modelgateway

// WriterToolsForProfile narrows the final validation to reading a known source
// range, reading offloaded artifacts, and reporting one grounded terminal result.
// Baseline and post-patch tests remain owned by Go. Other profiles retain the
// ordinary product inventory; this cannot add tool authority.
func WriterToolsForProfile(profile string) []string {
	if profile != "final-smoke" {
		return nil
	}
	return []string{"repo_read", "propose_patch", "cannot_determine", "read_file"}
}

func finalSmokeWriterInventory(inventory []string) bool {
	expected := WriterToolsForProfile("final-smoke")
	if len(inventory) != len(expected) {
		return false
	}
	seen := make(map[string]bool, len(inventory))
	for _, name := range inventory {
		if seen[name] {
			return false
		}
		seen[name] = true
	}
	for _, name := range expected {
		if !seen[name] {
			return false
		}
	}
	return true
}
