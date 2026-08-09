package clusterssh

import "context"

var _ Client = (*MockClient)(nil)

// MockClient records Run/Push calls and resolves output/error via Script,
// for unit-testing callers without a real cluster.
type MockClient struct {
	Runs   []string
	Pushes [][2]string
	// Script resolves a Run command to output lines and an error. A nil
	// Script (the default) produces no output and no error.
	Script func(cmd string) (lines []string, err error)
}

func (m *MockClient) Run(ctx context.Context, cmd string, onLine func(string)) error {
	m.Runs = append(m.Runs, cmd)
	if m.Script == nil {
		return nil
	}
	lines, err := m.Script(cmd)
	if onLine != nil {
		for _, l := range lines {
			onLine(l)
		}
	}
	return err
}

func (m *MockClient) Push(ctx context.Context, localPath, remotePath string) error {
	m.Pushes = append(m.Pushes, [2]string{localPath, remotePath})
	return nil
}

func (m *MockClient) Close() error {
	return nil
}
