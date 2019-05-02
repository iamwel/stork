package resourcecollector

import (
	"strings"

	"github.com/portworx/sched-ops/k8s"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/authentication/serviceaccount"
)

func (r *ResourceCollector) subjectInNamespace(subject *rbacv1.Subject, namespace string) (bool, error) {
	switch subject.Kind {
	case rbacv1.ServiceAccountKind:
		if subject.Namespace == namespace {
			return true, nil
		}
	case rbacv1.UserKind:
		userNamespace, _, err := serviceaccount.SplitUsername(subject.Name)
		if err != nil {
			return false, nil
		}
		if userNamespace == namespace {
			return true, nil
		}
	case rbacv1.GroupKind:
		groupNamespace := strings.TrimPrefix(subject.Name, serviceaccount.ServiceAccountUsernamePrefix)
		if groupNamespace == namespace {
			return true, nil
		}
	}
	return false, nil
}

func (r *ResourceCollector) clusterRoleBindingToBeCollected(
	labelSelectors map[string]string,
	object runtime.Unstructured,
	namespace string,
) (bool, error) {
	var crb rbacv1.ClusterRoleBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), &crb); err != nil {
		return false, err
	}
	// Check if there is a subject for the namespace which is
	// requested
	for _, subject := range crb.Subjects {
		collect, err := r.subjectInNamespace(&subject, namespace)
		if err != nil || collect {
			return collect, err
		}
	}
	return false, nil
}

func (r *ResourceCollector) clusterRoleToBeCollected(
	labelSelectors map[string]string,
	object runtime.Unstructured,
	namespace string,
) (bool, error) {
	metadata, err := meta.Accessor(object)
	if err != nil {
		return false, err
	}
	name := metadata.GetName()
	crbs, err := k8s.Instance().ListClusterRoleBindings()
	if err != nil {
		return false, err
	}
	// Find the corresponding ClusterRoleBinding and see
	// if it belongs to the requested namespace
	for _, crb := range crbs.Items {
		if crb.RoleRef.Name == name {
			for _, subject := range crb.Subjects {
				collect, err := r.subjectInNamespace(&subject, namespace)
				if err != nil || collect {
					return collect, err
				}
			}
		}
	}
	return false, nil
}

func (r *ResourceCollector) prepareClusterRoleBindingForCollection(
	object runtime.Unstructured,
	namespaces []string,
) error {
	var crb rbacv1.ClusterRoleBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), &crb); err != nil {
		return err
	}
	subjectsToCollect := make([]rbacv1.Subject, 0)
	// Check if there is a subject for the namespace which is requested
	for _, subject := range crb.Subjects {
		for _, ns := range namespaces {
			collect, err := r.subjectInNamespace(&subject, ns)
			if err != nil {
				return err
			}

			if collect {
				subjectsToCollect = append(subjectsToCollect, subject)
			}
		}
	}
	crb.Subjects = subjectsToCollect
	o, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&crb)
	if err != nil {
		return err
	}
	object.SetUnstructuredContent(o)

	return nil
}

func (r *ResourceCollector) prepareClusterRoleBindingForApply(
	object runtime.Unstructured,
	namespaceMappings map[string]string,
) error {
	var crb rbacv1.ClusterRoleBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), &crb); err != nil {
		return err
	}
	subjectsToApply := make([]rbacv1.Subject, 0)
	for sourceNamespace, destNamespace := range namespaceMappings {
		// Check if there is a subject for the namespace which is requested
		for _, subject := range crb.Subjects {
			collect, err := r.subjectInNamespace(&subject, sourceNamespace)
			if err != nil {
				return err
			}
			if !collect {
				continue
			}

			switch subject.Kind {
			case rbacv1.UserKind:
				_, username, err := serviceaccount.SplitUsername(subject.Name)
				if err != nil {
					return err
				}
				subject.Name = serviceaccount.MakeUsername(destNamespace, username)
			case rbacv1.GroupKind:
				subject.Name = serviceaccount.MakeNamespaceGroupName(destNamespace)
			case rbacv1.ServiceAccountKind:
				subject.Namespace = destNamespace
			}
			subjectsToApply = append(subjectsToApply, subject)
		}
	}
	crb.Subjects = subjectsToApply
	o, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&crb)
	if err != nil {
		return err
	}
	object.SetUnstructuredContent(o)

	return nil
}

func (r *ResourceCollector) mergeAndUpdateClusterRoleBinding(
	object runtime.Unstructured,
) error {
	var newCRB rbacv1.ClusterRoleBinding
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.UnstructuredContent(), &newCRB); err != nil {
		return err
	}

	currentCRB, err := k8s.Instance().GetClusterRoleBinding(newCRB.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			_, err = k8s.Instance().CreateClusterRoleBinding(&newCRB)
		}
		return err
	}

	// Map which will help eliminate duplicate subjects
	updatedSubjects := make(map[string]rbacv1.Subject)
	for _, subject := range currentCRB.Subjects {
		updatedSubjects[subject.String()] = subject
	}
	for _, subject := range newCRB.Subjects {
		updatedSubjects[subject.String()] = subject
	}
	currentCRB.Subjects = make([]rbacv1.Subject, 0)
	for _, subject := range updatedSubjects {
		currentCRB.Subjects = append(currentCRB.Subjects, subject)
	}

	_, err = k8s.Instance().UpdateClusterRoleBinding(currentCRB)
	return err
}
