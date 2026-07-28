package gitrepo

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type RemoteRepo struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
	Private  bool   `json:"private"`
}

const maxReposFetched = 500
const perPage = 100

// ListRepos lists repositories accessible to token on the given provider, so
// the frontend can offer a searchable picker instead of requiring the user
// to hand-type a clone URL. Only github/gitlab are supported, "other"
// providers have no known API shape and use a direct URL-entry form instead.
func ListRepos(provider Provider, token string) ([]RemoteRepo, error) {
	switch provider {
	case ProviderGitHub:
		return listGitHubRepos(token)
	case ProviderGitLab:
		return listGitLabRepos(token)
	default:
		return nil, errUnsupportedProvider
	}
}

func listGitHubRepos(token string) ([]RemoteRepo, error) {
	repos := []RemoteRepo{}

	for page := 1; len(repos) < maxReposFetched; page++ {
		url := fmt.Sprintf("https://api.github.com/user/repos?per_page=%d&page=%d&sort=updated&affiliation=owner,collaborator", perPage, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		var body []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
			Private  bool   `json:"private"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("github api returned status %d while listing repositories", resp.StatusCode)
		}

		if len(body) == 0 {
			break
		}

		for _, repo := range body {
			repos = append(repos, RemoteRepo{
				Name:     repo.Name,
				FullName: repo.FullName,
				CloneURL: repo.CloneURL,
				Private:  repo.Private,
			})
		}

		if len(body) < perPage {
			break
		}
	}

	return repos, nil
}

func listGitLabRepos(token string) ([]RemoteRepo, error) {
	repos := []RemoteRepo{}

	for page := 1; len(repos) < maxReposFetched; page++ {
		url := fmt.Sprintf("https://gitlab.com/api/v4/projects?membership=true&per_page=%d&page=%d&order_by=last_activity_at", perPage, page)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		var body []struct {
			Name              string `json:"name"`
			PathWithNamespace string `json:"path_with_namespace"`
			HTTPURLToRepo     string `json:"http_url_to_repo"`
			Visibility        string `json:"visibility"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, decodeErr
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("gitlab api returned status %d while listing repositories", resp.StatusCode)
		}

		if len(body) == 0 {
			break
		}

		for _, repo := range body {
			repos = append(repos, RemoteRepo{
				Name:     repo.Name,
				FullName: repo.PathWithNamespace,
				CloneURL: repo.HTTPURLToRepo,
				Private:  repo.Visibility != "public",
			})
		}

		if len(body) < perPage {
			break
		}
	}

	return repos, nil
}

// ValidateToken performs a cheap authenticated call to confirm a manually
// entered PAT actually works for the given provider before it's stashed in
// a session.
func ValidateToken(provider Provider, token string) error {
	var url string

	switch provider {
	case ProviderGitHub:
		url = "https://api.github.com/user"
	case ProviderGitLab:
		url = "https://gitlab.com/api/v4/user"
	default:
		return errUnsupportedProvider
	}

	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)

	resp, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token validation failed with status %d", resp.StatusCode)
	}

	return nil
}
