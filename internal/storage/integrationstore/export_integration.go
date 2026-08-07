//go:build integration

package integrationstore

func (s *Store) IntegrationSetTargetRefGenerator(generator func(string) (string, error)) {
	s.targetRefGenerator = generator
}
