package terraformpullrequest

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp"
	configv1alpha1 "github.com/padok-team/burrito/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/padok-team/burrito/internal/repository"
	repositorytypes "github.com/padok-team/burrito/internal/repository/types"
	log "github.com/sirupsen/logrus"
)

func (r *Reconciler) syncFromRepository(ctx context.Context, repositoryObj *configv1alpha1.TerraformRepository) (ctrl.Result, error) {
	log := log.WithContext(ctx)
	log.Infof("synchronizing pull requests for repository %s/%s", repositoryObj.Namespace, repositoryObj.Name)

	provider, err := r.getAPIProvider(repositoryObj)
	if err != nil {
		r.Recorder.Event(repositoryObj, corev1.EventTypeWarning, "Provider error", "Failed to get API provider for pull request synchronization")
		log.Errorf("failed to get API provider for repository %s/%s: %s", repositoryObj.Namespace, repositoryObj.Name, err)
		return ctrl.Result{RequeueAfter: r.Config.Controller.Timers.OnError}, err
	}

	remotePullRequests, err := provider.ListPullRequests(repositoryObj)
	if err != nil {
		r.Recorder.Event(repositoryObj, corev1.EventTypeWarning, "Reconciliation", "Failed to list open pull requests")
		log.Errorf("failed to list open pull requests for repository %s/%s: %s", repositoryObj.Namespace, repositoryObj.Name, err)
		return ctrl.Result{RequeueAfter: r.Config.Controller.Timers.OnError}, err
	}

	existingPullRequests := &configv1alpha1.TerraformPullRequestList{}
	err = r.Client.List(ctx, existingPullRequests)
	if err != nil {
		r.Recorder.Event(repositoryObj, corev1.EventTypeWarning, "Reconciliation", "Failed to list existing pull requests")
		log.Errorf("failed to list existing pull requests for repository %s/%s: %s", repositoryObj.Namespace, repositoryObj.Name, err)
		return ctrl.Result{RequeueAfter: r.Config.Controller.Timers.OnError}, err
	}

	desiredPullRequests := map[string]configv1alpha1.TerraformPullRequest{}
	for _, pullRequest := range remotePullRequests {
		desiredPullRequests[pullRequest.Name] = pullRequest
		if err := r.applyDesiredPullRequest(ctx, &pullRequest); err != nil {
			r.Recorder.Event(repositoryObj, corev1.EventTypeWarning, "Reconciliation", fmt.Sprintf("Failed to synchronize pull request %s", pullRequest.Name))
			log.Errorf("failed to synchronize pull request %s: %s", pullRequest.Name, err)
			return ctrl.Result{RequeueAfter: r.Config.Controller.Timers.OnError}, err
		}
	}

	for i := range existingPullRequests.Items {
		current := &existingPullRequests.Items[i]
		if !sameRepository(current, repositoryObj) {
			continue
		}
		if _, ok := desiredPullRequests[current.Name]; ok {
			continue
		}
		if err := r.deleteRemotePullRequest(ctx, current); err != nil {
			r.Recorder.Event(repositoryObj, corev1.EventTypeWarning, "Reconciliation", fmt.Sprintf("Failed to delete pull request %s", current.Name))
			log.Errorf("failed to delete pull request %s: %s", current.Name, err)
			return ctrl.Result{RequeueAfter: r.Config.Controller.Timers.OnError}, err
		}
	}

	return ctrl.Result{RequeueAfter: r.Config.Controller.Timers.RepositorySync}, nil
}

func (r *Reconciler) getAPIProvider(repositoryObj *configv1alpha1.TerraformRepository) (repositorytypes.APIProvider, error) {
	if r.APIProviderFactory != nil {
		return r.APIProviderFactory(repositoryObj)
	}
	return repository.GetAPIProviderFromRepository(r.Credentials, repositoryObj)
}

func (r *Reconciler) applyDesiredPullRequest(ctx context.Context, desired *configv1alpha1.TerraformPullRequest) error {
	current := &configv1alpha1.TerraformPullRequest{}
	err := r.Client.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, current)
	if apierrors.IsNotFound(err) {
		err = r.Client.Create(ctx, desired.DeepCopy())
		if apierrors.IsAlreadyExists(err) {
			current = &configv1alpha1.TerraformPullRequest{}
			err = r.Client.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, current)
			if err != nil {
				return err
			}
			current.Spec = desired.Spec
			current.Annotations = desired.Annotations
			return r.Client.Update(ctx, current)
		}
		return err
	}
	if err != nil {
		return err
	}
	if cmp.Diff(current.Spec, desired.Spec) == "" && cmp.Diff(current.Annotations, desired.Annotations) == "" {
		return nil
	}
	current.Spec = desired.Spec
	current.Annotations = desired.Annotations
	return r.Client.Update(ctx, current)
}

func (r *Reconciler) deleteRemotePullRequest(ctx context.Context, pullRequest *configv1alpha1.TerraformPullRequest) error {
	err := r.Client.Delete(ctx, pullRequest)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func sameRepository(pullRequest *configv1alpha1.TerraformPullRequest, repositoryObj *configv1alpha1.TerraformRepository) bool {
	return pullRequest.Spec.Repository.Name == repositoryObj.Name && pullRequest.Spec.Repository.Namespace == repositoryObj.Namespace
}
