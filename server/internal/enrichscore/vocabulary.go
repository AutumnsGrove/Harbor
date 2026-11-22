// The tag vocabulary the write call is given. Production loads it from the
// library (enrich calls Store.TagVocabulary(50)); the scored set has no library,
// so it takes a file — or nothing, which is what a fresh install sees.
package enrichscore

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// vocabularyLimit matches the pipeline's request: TagVocabulary(50).
const vocabularyLimit = 50

// LoadVocabulary reads one tag per line, `#` comments and blanks ignored,
// normalised the way the store normalises them. It is the scored run's stand-in
// for a library's canonical tags; without one the prompt carries no vocabulary
// hint at all, which is a fresh library's prompt, not a mature one's.
func LoadVocabulary(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("enrichscore: reading the vocabulary: %w", err)
	}
	defer file.Close()

	seen := map[string]bool{}
	var tags []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := store.NormaliseTag(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tags = append(tags, name)
		if len(tags) == vocabularyLimit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("enrichscore: reading the vocabulary: %w", err)
	}
	return tags, nil
}
