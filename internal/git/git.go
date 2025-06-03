package git

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Repo represents a git repository
type Repo interface {
	// Add adds a file to the repository
	Add(file string) error
	// AddNote adds a note to the latest commit
	AddNote(note string) error
	// Commit commits the changes to the repository
	Commit(message string, allowEmpty bool) error
	// GetNoteContents gets the contents of all notes in the repository, calling
	// the handle for each
	GetNoteContents(handle func(string)) error
	seal()
}

func (r *repo) seal() {}

type repo struct {
	path string
}

func (r *repo) Add(file string) error {
	cmd := exec.Command("git", "add", file)
	cmd.Dir = r.path
	return cmd.Run()
}

func (r *repo) Commit(message string, allowEmpty bool) error {
	var args []string
	if allowEmpty {
		args = []string{"commit", "-m", message, "--allow-empty"}
	} else {
		args = []string{"commit", "-m", message}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = r.path
	return cmd.Run()
}

func (r *repo) addGitIgnore(gitIgnore string) error {
	gitIgnorePath := filepath.Join(r.path, ".gitignore")
	if _, err := os.Stat(gitIgnorePath); !os.IsNotExist(err) {
		log.Printf("gitignore already exists in %s", r.path)
	} else {
		err := os.WriteFile(gitIgnorePath, []byte(gitIgnore), 0644)
		if err != nil {
			return fmt.Errorf("failed to write gitignore to %s: %v", gitIgnorePath, err)
		}
	}

	err := r.Add(gitIgnorePath)
	if err != nil {
		return fmt.Errorf("failed to add gitignore to %s: %v", gitIgnorePath, err)
	}

	err = r.Commit("Add gitignore", false)
	if err != nil {
		return fmt.Errorf("failed to commit gitignore to %s: %v", gitIgnorePath, err)
	}

	return nil
}

func initRepo(path string, gitIgnore string) (*repo, error) {
	cmd := exec.Command("git", "init")
	cmd.Dir = path

	err := cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to init repo %s: %v", path, err)
	}

	repo := repo{path: path}

	if gitIgnore != "" {
		err = repo.addGitIgnore(gitIgnore)
		if err != nil {
			return nil, fmt.Errorf("failed to add gitignore to %s: %v", path, err)
		}
	}

	return &repo, nil
}

func NewRepo(path string, gitIgnore string) (Repo, error) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("repo %s does not exist", path)
	}

	if !fi.IsDir() {
		return nil, fmt.Errorf("path %s is not a directory", path)
	}

	if _, err := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(err) {
		repo, err := initRepo(path, gitIgnore)
		if err != nil {
			return nil, err
		}

		return repo, nil
	}

	log.Printf("will manage existing repo %s", path)
	repo, err := initRepo(path, gitIgnore)
	if err != nil {
		return nil, err
	}

	return repo, nil
}

func (r *repo) AddNote(note string) error {
	// Add the vector as a note to the latest commit
	noteCmd := exec.Command("git", "notes", "append", "-m", note, "HEAD")
	noteCmd.Dir = r.path

	if err := noteCmd.Run(); err != nil {
		return fmt.Errorf("failed to add note: %v", err)
	}

	return nil
}

func (r *repo) getNotes() ([]string, error) {
	noteCmd := exec.Command("git", "notes", "list")
	noteCmd.Dir = r.path

	stdout, err := noteCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	if err := noteCmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start notes command: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	result := make([]string, 0)

	for scanner.Scan() {
		line := scanner.Text()
		if idx := strings.Index(line, " "); idx > 0 {
			result = append(result, line[:idx])
		} else if line != "" {
			result = append(result, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan notes: %v", err)
	}
	if err := noteCmd.Wait(); err != nil {
		return nil, fmt.Errorf("failed to wait for notes command: %v", err)
	}

	return result, nil
}

func (r *repo) getNoteContents(noteRefs []string, handle func(string)) error {
	if len(noteRefs) == 0 {
		return nil
	}

	catCmd := exec.Command("git", "cat-file", "--batch=")
	catCmd.Dir = r.path

	stdin, err := catCmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %v", err)
	}

	stdout, err := catCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %v", err)
	}

	if err := catCmd.Start(); err != nil {
		return fmt.Errorf("failed to start cat-file command: %v", err)
	}

	// Write all note refs to stdin
	for _, ref := range noteRefs {
		if _, err := fmt.Fprintln(stdin, ref); err != nil {
			stdin.Close()
			return fmt.Errorf("failed to write to stdin: %v", err)
		}
	}
	stdin.Close()

	// Read the contents from stdout
	scanner := bufio.NewScanner(stdout)

	for scanner.Scan() {
		content := scanner.Text()

		if content != "" {
			handle(content)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to scan cat-file output: %v", err)
	}

	if err := catCmd.Wait(); err != nil {
		return fmt.Errorf("failed to wait for cat-file command: %v", err)
	}

	return nil
}

func (r *repo) GetNoteContents(handle func(string)) error {
	noteRefs, err := r.getNotes()
	if err != nil {
		return fmt.Errorf("failed to get notes: %v", err)
	}

	return r.getNoteContents(noteRefs, handle)
}
