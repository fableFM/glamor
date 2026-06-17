package codex

import "strings"

// Input is a prompt plus optional local image paths.
type Input struct {
	Prompt string
	Images []string
}

// Text creates text-only input.
func Text(prompt string) Input {
	return Input{Prompt: prompt, Images: []string{}}
}

// WithImage adds a local image path to the input.
func (i Input) WithImage(path string) Input {
	i.Images = append(i.Images, path)
	return i
}

// FromParts creates input from text snippets and image paths.
func FromParts(texts []string, images []string) Input {
	return Input{
		Prompt: strings.Join(texts, "\n\n"),
		Images: append([]string{}, images...),
	}
}
