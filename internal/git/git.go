package git

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

type Repo struct {
	path string
}

func (r Repo) Add(file string) error {
	cmd := exec.Command("git", "add", file)
	cmd.Dir = r.path
	return cmd.Run()
}

func (r Repo) Commit(message string) error {
	cmd := exec.Command("git", "commit", "-m", message)
	cmd.Dir = r.path
	return cmd.Run()
}

func (r Repo) addGitIgnore(gitIgnore string) error {
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

	err = r.Commit("Add gitignore")
	if err != nil {
		return fmt.Errorf("failed to commit gitignore to %s: %v", gitIgnorePath, err)
	}

	return nil
}

func initRepo(path string, gitIgnore string) (Repo, error) {
	cmd := exec.Command("git", "init")
	cmd.Dir = path

	err := cmd.Run()
	if err != nil {
		return Repo{}, fmt.Errorf("failed to init repo %s: %v", path, err)
	}

	repo := Repo{path: path}

	err = repo.addGitIgnore(gitIgnore)
	if err != nil {
		return Repo{}, fmt.Errorf("failed to add gitignore to %s: %v", path, err)
	}

	return repo, nil
}

func NewRepo(path string, gitIgnore string) (Repo, error) {
	fi, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Repo{}, fmt.Errorf("repo %s does not exist", path)
	}

	if !fi.IsDir() {
		return Repo{}, fmt.Errorf("path %s is not a directory", path)
	}

	if _, err := os.Stat(filepath.Join(path, ".git")); os.IsNotExist(err) {
		return initRepo(path, gitIgnore)
	}

	log.Printf("will manage existing repo %s", path)
	return Repo{path: path}, nil
}
