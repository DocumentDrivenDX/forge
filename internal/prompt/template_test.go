package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadTemplate(t *testing.T) {
	dir := t.TempDir()

	t.Run("with frontmatter", func(t *testing.T) {
		path := filepath.Join(dir, "review.md")
		content := "---\nname: code-review\ndescription: Review code changes\n---\nReview the following code:\n$@"
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

		tmpl, err := LoadTemplate(path)
		require.NoError(t, err)
		assert.Equal(t, "code-review", tmpl.Name)
		assert.Equal(t, "Review code changes", tmpl.Description)
		assert.Equal(t, "Review the following code:\n$@", tmpl.Content)
	})

	t.Run("without frontmatter", func(t *testing.T) {
		path := filepath.Join(dir, "simple.md")
		require.NoError(t, os.WriteFile(path, []byte("Just a prompt."), 0o644))

		tmpl, err := LoadTemplate(path)
		require.NoError(t, err)
		assert.Equal(t, "simple", tmpl.Name)
		assert.Equal(t, "", tmpl.Description)
		assert.Equal(t, "Just a prompt.", tmpl.Content)
	})
}

func TestLoadTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("template a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("template b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skip.txt"), []byte("not a template"), 0o644))

	templates, err := LoadTemplates(dir)
	require.NoError(t, err)
	assert.Len(t, templates, 2)
}

func TestLoadTemplates_MissingDir(t *testing.T) {
	templates, err := LoadTemplates("/nonexistent/path")
	require.NoError(t, err) // missing dir is not an error
	assert.Nil(t, templates)
}

func TestSubstituteArgs(t *testing.T) {
	t.Run("positional args", func(t *testing.T) {
		result := SubstituteArgs("Hello $1, meet $2.", []string{"Alice", "Bob"})
		assert.Equal(t, "Hello Alice, meet Bob.", result)
	})

	t.Run("missing positional arg returns empty", func(t *testing.T) {
		result := SubstituteArgs("Hello $1 and $2.", []string{"Alice"})
		assert.Equal(t, "Hello Alice and .", result)
	})

	t.Run("$@ replaces with all args", func(t *testing.T) {
		result := SubstituteArgs("Args: $@", []string{"a", "b", "c"})
		assert.Equal(t, "Args: a b c", result)
	})

	t.Run("$ARGUMENTS replaces with all args", func(t *testing.T) {
		result := SubstituteArgs("Args: $ARGUMENTS", []string{"x", "y"})
		assert.Equal(t, "Args: x y", result)
	})

	t.Run("${@:N} slices from Nth", func(t *testing.T) {
		result := SubstituteArgs("Rest: ${@:2}", []string{"a", "b", "c", "d"})
		assert.Equal(t, "Rest: b c d", result)
	})

	t.Run("${@:N:L} slices N with length L", func(t *testing.T) {
		result := SubstituteArgs("Slice: ${@:2:2}", []string{"a", "b", "c", "d"})
		assert.Equal(t, "Slice: b c", result)
	})

	t.Run("${@:N} beyond length returns empty", func(t *testing.T) {
		result := SubstituteArgs("Rest: ${@:10}", []string{"a"})
		assert.Equal(t, "Rest: ", result)
	})

	t.Run("combined patterns", func(t *testing.T) {
		result := SubstituteArgs("File: $1, rest: ${@:2}", []string{"main.go", "line1", "line2"})
		assert.Equal(t, "File: main.go, rest: line1 line2", result)
	})

	t.Run("no args", func(t *testing.T) {
		result := SubstituteArgs("No args: $@ end.", nil)
		assert.Equal(t, "No args:  end.", result)
	})
}
