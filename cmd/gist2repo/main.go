package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/go-github/v28/github"
	"golang.org/x/oauth2"
)

const (
	SYNC2REPO_TOKEN_NAME = "SYNC2REPO_TOKEN"
)

var (
	token    string
	repoPath string
	userName string
)

func parseFlags() {
	flag.StringVar(&token, "token", "", "OAuth token https://github.com/settings/tokens")
	flag.StringVar(&repoPath, "repo", "", "path to a destination repository on FS")
	flag.StringVar(&userName, "user", "", "name of user of source gists")
	flag.Parse()
}

func parseEnvs() {
	t := os.Getenv(SYNC2REPO_TOKEN_NAME)
	if t != "" {
		token = t
	}
}

func getClient(ctx context.Context, token string) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)

	return github.NewClient(tc)
}

func listGists(ctx context.Context, client *github.Client, user string) (<-chan *github.Gist, <-chan error) {
	gistsCh := make(chan *github.Gist)
	errCh := make(chan error, 1)

	go func() {
		opt := &github.GistListOptions{}
		var nextPage, lastPage = 0, 1
		for lastPage != 0 && nextPage <= lastPage {
			opt.Page = nextPage
			gists, resp, err := client.Gists.List(ctx, user, opt)
			if err != nil {
				errCh <- err
				break
			}
			lastPage = resp.LastPage
			nextPage = resp.NextPage

			for _, gist := range gists {
				gistsCh <- gist
			}
		}
		close(errCh)
		close(gistsCh)
	}()

	return gistsCh, errCh
}

func cloneRepos(baseDir string, repos <-chan string) (<-chan string, <-chan error) {
	pathCh := make(chan string)
	errCh := make(chan error, 1)

	go func() {
		limit := make(chan struct{}, 30)
		wg := sync.WaitGroup{}

		for repo := range repos {
			wg.Add(1)
			go func(r string) {
				limit <- struct{}{}
				path := filepath.Join(baseDir, repoPathToName(r))
				if err := execGit("clone", r, path); err != nil {
					errCh <- err
				}
				pathCh <- path
				<-limit
				wg.Done()
			}(repo)
		}

		wg.Wait()
		close(limit)
		close(pathCh)
		close(errCh)
	}()

	return pathCh, errCh
}

func reposUrl(gists <-chan *github.Gist) <-chan string {
	urlCh := make(chan string)

	go func() {
		for gist := range gists {
			urlCh <- gist.GetGitPullURL()
		}
		close(urlCh)
	}()

	return urlCh
}

func repoPathToName(repo string) string {
	return strings.TrimRight(filepath.Base(repo), ".git")
}

func execGit(args ...string) error {
	cmd := exec.Command("git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v", output, err)
	}
	return nil
}

func mergeRepos(dstRepoPath string, repos <-chan string) <-chan error {
	errCh := make(chan error, 1)

	currDir, err := filepath.Abs(".")
	if err != nil {
		errCh <- err
		close(errCh)
	}
	if err := os.Chdir(dstRepoPath); err != nil {
		errCh <- err
		close(errCh)
	}

	go func() {
		for repo := range repos {
			remoteName := repoPathToName(repo)

			stages := [][]string{
				// add remote
				{"remote", "add", remoteName, repo},
				// fetch remote
				{"fetch", remoteName},
				// merge remote
				{"merge", "--allow-unrelated-histories", "-m", "move gists to repo", remoteName + "/master"},
				// remove remote
				{"remote", "rm", remoteName},
			}

			for _, args := range stages {
				if err := execGit(args...); err != nil {
					errCh <- fmt.Errorf("%s: %v", repo, err)
					continue
				}
			}
		}

		if err := os.Chdir(currDir); err != nil {
			errCh <- err
		}
		close(errCh)
	}()

	return errCh
}

func mergeErrorChs(channels ...<-chan error) chan error {
	out := make(chan error)
	wg := sync.WaitGroup{}

	for _, ch := range channels {
		wg.Add(1)
		go func(ch <-chan error) {
			for err := range ch {
				out <- err
			}
			wg.Done()
		}(ch)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

func main() {
	parseEnvs()
	parseFlags()

	if userName == "" || token == "" || repoPath == "" {
		log.Fatal("One or more arguments have not been passed")
	}

	ctx := context.Background()
	client := getClient(ctx, token)

	reposDir, err := ioutil.TempDir("", "")
	if err != nil {
		log.Fatalf("Cannot create temp directory: %v", err)
	}
	defer os.RemoveAll(reposDir)

	gistsCh, gistsErrCh := listGists(ctx, client, userName)
	pathCh, reposErrCh := cloneRepos(reposDir, reposUrl(gistsCh))
	mergeErrCh := mergeRepos(repoPath, pathCh)

	for err := range mergeErrorChs(gistsErrCh, reposErrCh, mergeErrCh) {
		log.Println(err)
	}
}
