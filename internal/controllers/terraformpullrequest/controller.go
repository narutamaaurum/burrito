package terraformpullrequest

import (
	"context"

	"github.com/google/go-cmp/cmp"
	"github.com/padok-team/burrito/internal/burrito/config"
	datastore "github.com/padok-team/burrito/internal/datastore/client"
	"github.com/padok-team/burrito/internal/repository/credentials"
	repositorytypes "github.com/padok-team/burrito/internal/repository/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	log "github.com/sirupsen/logrus"

	configv1alpha1 "github.com/padok-team/burrito/api/v1alpha1"
)

// Reconciler reconciles a TerraformPullRequest object
type Reconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	Config             *config.Config
	Credentials        *credentials.CredentialStore
	APIProviderFactory func(repository *configv1alpha1.TerraformRepository) (repositorytypes.APIProvider, error)
	Recorder           record.EventRecorder
	Datastore          datastore.Client
}

//+kubebuilder:rbac:groups=config.terraform.padok.cloud,resources=terraformpullrequests,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=config.terraform.padok.cloud,resources=terraformpullrequests/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=config.terraform.padok.cloud,resources=terraformpullrequests/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.WithContext(ctx)
	log.Infof("starting reconciliation for pull request %s/%s ...", req.Namespace, req.Name)
	repository := &configv1alpha1.TerraformRepository{}
	repositoryErr := r.Client.Get(ctx, req.NamespacedName, repository)
	if repositoryErr == nil {
		return r.syncFromRepository(ctx, repository)
	}
	if !errors.IsNotFound(repositoryErr) {
		log.Errorf("failed to get TerraformRepository: %s", repositoryErr)
		return ctrl.Result{}, repositoryErr
	}

	pr := &configv1alpha1.TerraformPullRequest{}
	err := r.Client.Get(ctx, req.NamespacedName, pr)
	if errors.IsNotFound(err) {
		log.Errorf("resource not found. Ignoring since object must be deleted: %s", err)
		return ctrl.Result{}, nil
	}
	if err != nil {
		log.Errorf("failed to get TerraformPullRequest: %s", err)
		return ctrl.Result{}, err
	}
	repository = &configv1alpha1.TerraformRepository{}
	err = r.Client.Get(ctx, types.NamespacedName{
		Name:      pr.Spec.Repository.Name,
		Namespace: pr.Spec.Repository.Namespace,
	}, repository)
	if errors.IsNotFound(err) {
		log.Errorf("repository not found. object must not be configured correctly: %s", err)
		return ctrl.Result{}, nil
	}
	if err != nil {
		log.Errorf("failed to get TerraformRepository: %s", err)
		return ctrl.Result{}, err
	}

	state := r.GetState(ctx, pr)
	result := state.Handler(ctx, r, repository, pr)
	pr.Status = state.Status
	err = r.Client.Status().Update(ctx, pr)
	if err != nil {
		r.Recorder.Event(pr, corev1.EventTypeWarning, "Reconciliation", "Could not update pull request status")
		log.Errorf("could not update pull request %s status: %s", pr.Name, err)
	}
	log.Infof("finished reconciliation cycle for pull request %s/%s", pr.Namespace, pr.Name)
	return result, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&configv1alpha1.TerraformPullRequest{}).
		Watches(&configv1alpha1.TerraformRepository{}, handler.EnqueueRequestsFromMapFunc(
			func(ctx context.Context, obj client.Object) []reconcile.Request {
				repository := obj.(*configv1alpha1.TerraformRepository)
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: repository.Namespace, Name: repository.Name}}}
			},
		)).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.Config.Controller.MaxConcurrentReconciles}).
		WithEventFilter(ignorePredicate()).
		Complete(r)
}

func ignorePredicate() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if _, ok := e.ObjectOld.(*configv1alpha1.TerraformRepository); ok {
				return true
			}
			if _, ok := e.ObjectNew.(*configv1alpha1.TerraformRepository); ok {
				return true
			}
			// Update only if generation or annotations change, filter out anything else.
			// We only need to check generation or annotations change here, because it is only
			// updated on spec changes. On the other hand RevisionVersion
			// changes also on status changes. We want to omit reconciliation
			// for status updates.
			return (e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()) ||
				cmp.Diff(e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations()) != ""
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Evaluates to false if the object has been confirmed deleted.
			return !e.DeleteStateUnknown
		},
	}
}
