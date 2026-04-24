package gitlab

import (
	"fmt"
	"strconv"

	configv1alpha1 "github.com/padok-team/burrito/api/v1alpha1"
	"github.com/padok-team/burrito/internal/annotations"
	"github.com/padok-team/burrito/internal/controllers/terraformpullrequest/comment"
	log "github.com/sirupsen/logrus"
	gitlab "gitlab.com/gitlab-org/api/client-go/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type APIProvider struct {
	client *gitlab.Client
}

func (api *APIProvider) GetChanges(repository *configv1alpha1.TerraformRepository, pr *configv1alpha1.TerraformPullRequest) ([]string, error) {
	id, err := strconv.ParseInt(pr.Spec.ID, 10, 64)
	if err != nil {
		log.Errorf("Error while parsing Gitlab merge request ID: %s", err)
		return []string{}, err
	}
	listOpts := gitlab.ListMergeRequestDiffsOptions{
		ListOptions: gitlab.ListOptions{
			PerPage: 20,
		},
	}
	var changes []string
	for {
		diffs, resp, err := api.client.MergeRequests.ListMergeRequestDiffs(getGitlabNamespacedName(repository.Spec.Repository.Url), id, &listOpts)
		if err != nil {
			log.Errorf("Error while getting merge request changes: %s", err)
			return []string{}, err
		}
		for _, change := range diffs {
			changes = append(changes, change.NewPath)
		}
		if resp.NextPage == 0 {
			break
		}
		listOpts.Page = resp.NextPage
	}
	return changes, nil
}

func (api *APIProvider) Comment(repository *configv1alpha1.TerraformRepository, pr *configv1alpha1.TerraformPullRequest, comment comment.Comment) error {
	body, err := comment.Generate(pr.Annotations[annotations.LastBranchCommit])
	if err != nil {
		log.Errorf("Error while generating comment: %s", err)
		return err
	}
	id, err := strconv.ParseInt(pr.Spec.ID, 10, 64)
	if err != nil {
		log.Errorf("Error while parsing Gitlab merge request ID: %s", err)
		return err
	}
	_, _, err = api.client.Notes.CreateMergeRequestNote(getGitlabNamespacedName(repository.Spec.Repository.Url), id, &gitlab.CreateMergeRequestNoteOptions{
		Body: gitlab.Ptr(body),
	})
	if err != nil {
		log.Errorf("Error while creating merge request note: %s", err)
		return err
	}
	return nil
}

func (api *APIProvider) ListPullRequests(repository *configv1alpha1.TerraformRepository) ([]configv1alpha1.TerraformPullRequest, error) {
	state := "opened"
	listOpts := &gitlab.ListProjectMergeRequestsOptions{
		State: &state,
		ListOptions: gitlab.ListOptions{
			PerPage: 100,
		},
	}

	var pullRequests []configv1alpha1.TerraformPullRequest
	for {
		mergeRequests, resp, err := api.client.MergeRequests.ListProjectMergeRequests(getGitlabNamespacedName(repository.Spec.Repository.Url), listOpts)
		if err != nil {
			return nil, err
		}
		for _, mergeRequest := range mergeRequests {
			if mergeRequest == nil {
				continue
			}
			pullRequests = append(pullRequests, configv1alpha1.TerraformPullRequest{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("%s-%d", repository.Name, mergeRequest.IID),
					Namespace: repository.Namespace,
					Annotations: map[string]string{
						annotations.LastBranchCommit: mergeRequest.SHA,
					},
				},
				Spec: configv1alpha1.TerraformPullRequestSpec{
					Branch: mergeRequest.SourceBranch,
					Base:   mergeRequest.TargetBranch,
					ID:     strconv.FormatInt(mergeRequest.IID, 10),
					Repository: configv1alpha1.TerraformLayerRepository{
						Name:      repository.Name,
						Namespace: repository.Namespace,
					},
				},
			})
		}
		if resp.NextPage == 0 {
			break
		}
		listOpts.Page = resp.NextPage
	}

	return pullRequests, nil
}
