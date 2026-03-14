package controllers

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tofuv1alpha1 "github.com/twiechert/tofu-k8s-operator/api/v1alpha1"
)

type StandardValidator struct {
	Image   string
	Command string
}

var standardValidators = map[string]StandardValidator{
	"tflint":  {Image: "ghcr.io/terraform-linters/tflint:latest", Command: "tflint --init && tflint"},
	"checkov": {Image: "bridgecrew/checkov:latest", Command: "checkov -d . --framework terraform --compact"},
	"trivy":   {Image: "aquasec/trivy:latest", Command: "trivy config ."},
}

// addValidationToJob appends validation init containers to a Job based on the project's validation steps.
func addValidationToJob(job *batchv1.Job, project *tofuv1alpha1.TofuProject, mainImage string, gitMode bool, source *tofuv1alpha1.GitSource) error {
	if project.Spec.Validation == nil || len(project.Spec.Validation.Steps) == 0 {
		return nil
	}

	for _, step := range project.Spec.Validation.Steps {
		var img, cmd string

		if step.Standard != "" {
			sv, ok := standardValidators[step.Standard]
			if !ok {
				return fmt.Errorf("unknown standard validator %q", step.Standard)
			}
			img = sv.Image
			cmd = sv.Command
		} else if step.Custom != nil {
			cmd = step.Custom.Command
			img = step.Custom.Image
			if img == "" {
				img = mainImage
			}
		} else {
			return fmt.Errorf("validation step %q must set either standard or custom", step.Name)
		}

		copyScript := "cp /tf-config/* /tmp/validate/"
		if gitMode && source != nil {
			path := source.Path
			if path == "" {
				path = "."
			}
			copyScript = fmt.Sprintf("cp -r /git-repo/%s/. /tmp/validate/\ncp /tf-config/* /tmp/validate/", path)
		}

		script := fmt.Sprintf(`set -euo pipefail
mkdir -p /tmp/validate
%s
cd /tmp/validate
%s
`, copyScript, cmd)

		container := corev1.Container{
			Name:       fmt.Sprintf("validate-%s", step.Name),
			Image:      img,
			Command:    []string{"/bin/sh", "-c", script},
			WorkingDir: "/tmp/validate",
			VolumeMounts: []corev1.VolumeMount{{
				Name:      "tf-config",
				MountPath: "/tf-config",
				ReadOnly:  true,
			}},
		}

		if gitMode {
			container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
				Name:      "git-repo",
				MountPath: "/git-repo",
				ReadOnly:  true,
			})
		}

		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, container)
	}

	return nil
}

// buildPlanJob creates a Job that runs `tofu plan`.
func buildPlanJob(project tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	backoff := int32(0)
	workspace := project.Spec.Workspace
	gitMode := isGitSource(program)
	validate := tofuValidateEnabled(&project)
	cmd := []string{"/bin/sh", "-c", renderPlanCommand(workspace, gitMode, program.Spec.Source, project.Spec.IgnoreProviders, validate)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "plan",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    cmd,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	if gitMode {
		initContainer := gitCloneInitContainer(program.Spec.Source)
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, initContainer)
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "git-repo",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "git-repo", MountPath: "/git-repo", ReadOnly: true},
		)
		if program.Spec.Source.CredentialsSecretRef != nil {
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "git-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: program.Spec.Source.CredentialsSecretRef.Name,
					},
				},
			})
		}
	}

	return job
}

// buildApplyJob creates an apply Job that always uses -auto-approve (for plan-approve flow).
func buildApplyJob(project tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	// Force auto-approve since the plan has already been reviewed
	projectCopy := project
	projectCopy.Spec.AutoApprove = true
	job := buildJob(projectCopy, jobName, cmName, image, program, saName)
	return job
}

func buildJob(project tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	backoff := int32(0)
	workspace := project.Spec.Workspace
	gitMode := isGitSource(program)
	validate := tofuValidateEnabled(&project)
	cmd := []string{"/bin/sh", "-c", renderCommand(workspace, project.Spec.AutoApprove, gitMode, program.Spec.Source, project.Spec.IgnoreProviders, validate)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "apply",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    cmd,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	if gitMode {
		initContainer := gitCloneInitContainer(program.Spec.Source)
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, initContainer)
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "git-repo",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "git-repo", MountPath: "/git-repo", ReadOnly: true},
		)
		if program.Spec.Source.CredentialsSecretRef != nil {
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "git-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: program.Spec.Source.CredentialsSecretRef.Name,
					},
				},
			})
		}
	}

	return job
}

func gitCloneInitContainer(source *tofuv1alpha1.GitSource) corev1.Container {
	ref := source.Ref
	if ref == "" {
		ref = "main"
	}
	// Clone script supports two auth modes detected from env/volume:
	//   1. HTTPS token (PAT / GitHub App): Secret key "token"
	//   2. SSH deploy key: Secret key "sshPrivateKey" (+ optional "known_hosts")
	// Falls back to unauthenticated clone for public repos.
	script := fmt.Sprintf(`set -euo pipefail
REPO_URL="%s"
if [ -n "${GIT_TOKEN:-}" ]; then
  REPO_URL=$(echo "$REPO_URL" | sed "s|https://|https://x-access-token:${GIT_TOKEN}@|")
elif [ -f /git-credentials/sshPrivateKey ]; then
  mkdir -p ~/.ssh
  cp /git-credentials/sshPrivateKey ~/.ssh/id_key
  chmod 600 ~/.ssh/id_key
  if [ -f /git-credentials/known_hosts ]; then
    cp /git-credentials/known_hosts ~/.ssh/known_hosts
  else
    export GIT_SSH_COMMAND="ssh -o StrictHostKeyChecking=no -i ~/.ssh/id_key"
  fi
  if [ -z "${GIT_SSH_COMMAND:-}" ]; then
    export GIT_SSH_COMMAND="ssh -i ~/.ssh/id_key"
  fi
fi
git clone --branch %s --depth 1 "$REPO_URL" /git-repo
cd /git-repo && git rev-parse HEAD > /git-repo/.git-commit-sha
`, source.URL, shellEscape(ref))

	container := corev1.Container{
		Name:    "git-clone",
		Image:   "alpine/git:latest",
		Command: []string{"/bin/sh", "-c", script},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "git-repo",
			MountPath: "/git-repo",
		}},
	}

	if source.CredentialsSecretRef != nil {
		secretName := source.CredentialsSecretRef.Name
		// Mount token as env var for HTTPS auth
		optional := true
		container.Env = []corev1.EnvVar{{
			Name: "GIT_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  "token",
					Optional:             &optional,
				},
			},
		}}
		// Mount full Secret as volume for SSH key access
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "git-credentials",
			MountPath: "/git-credentials",
			ReadOnly:  true,
		})
	}

	return container
}

func buildDestroyJob(project *tofuv1alpha1.TofuProject, jobName, cmName, image string, program *tofuv1alpha1.TofuProgram, saName string) *batchv1.Job {
	var source *tofuv1alpha1.GitSource
	if program != nil && isGitSource(program) {
		source = program.Spec.Source
	}
	return buildDestroyJobWithSource(project, jobName, cmName, image, source, saName)
}

// buildDestroyJobWithSource creates a destroy Job using an explicit GitSource (which may contain a pinned commit SHA).
func buildDestroyJobWithSource(project *tofuv1alpha1.TofuProject, jobName, cmName, image string, source *tofuv1alpha1.GitSource, saName string) *batchv1.Job {
	backoff := int32(0)
	gitMode := source != nil
	validate := tofuValidateEnabled(project)
	cmd := []string{"/bin/sh", "-c", renderDestroyCommand(project.Spec.Workspace, gitMode, source, project.Spec.IgnoreProviders, validate)}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "destroy",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    cmd,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}

	if gitMode {
		initContainer := gitCloneInitContainer(source)
		job.Spec.Template.Spec.InitContainers = append(job.Spec.Template.Spec.InitContainers, initContainer)
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
			Name: "git-repo",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "git-repo", MountPath: "/git-repo", ReadOnly: true},
		)
		if source.CredentialsSecretRef != nil {
			job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
				Name: "git-credentials",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: source.CredentialsSecretRef.Name,
					},
				},
			})
		}
	}

	return job
}

// buildForceUnlockJob creates a Job that runs `tofu force-unlock -force`.
func buildForceUnlockJob(project *tofuv1alpha1.TofuProject, jobName, cmName, image, saName string) *batchv1.Job {
	backoff := int32(0)
	deadline := int64(300) // 5 minutes

	copyStep := "cp /tf-config/* /work/"
	stripSteps := renderStripBackendStep()

	script := fmt.Sprintf(`set -euo pipefail
%s
%stofu init -input=false
tofu force-unlock -force
`, copyStep, stripSteps)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: project.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "tofu-k8s-operator",
				"tofu.example.com/project":     project.Name,
				"tofu.example.com/job-type":    "force-unlock",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoff,
			ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: saName,
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{{
						Name:       "tofu",
						Image:      image,
						WorkingDir: "/work",
						Command:    []string{"/bin/sh", "-c", script},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "tf-config",
							MountPath: "/tf-config",
							ReadOnly:  true,
						}, {
							Name:      "work",
							MountPath: "/work",
						}},
					}},
					Volumes: []corev1.Volume{{
						Name: "tf-config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
							},
						},
					}, {
						Name: "work",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					}},
				},
			},
		},
	}
}

// setJobTimeout sets ActiveDeadlineSeconds on the Job spec based on the project's jobTimeout.
func setJobTimeout(job *batchv1.Job, project *tofuv1alpha1.TofuProject) {
	timeout := parseJobTimeout(project.Spec.JobTimeout)
	seconds := int64(timeout.Seconds())
	job.Spec.ActiveDeadlineSeconds = &seconds
}

// addCacheToJob injects the cache PVC volume, mount, and env var into a Job.
// When moduleCache is true, an additional subPath mount is added for module caching.
func addCacheToJob(job *batchv1.Job, pvcName string, moduleCache bool) {
	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{
		Name: "plugin-cache",
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvcName,
			},
		},
	})
	job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
		job.Spec.Template.Spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{
			Name:      "plugin-cache",
			MountPath: "/plugin-cache",
			SubPath:   "providers",
		},
	)
	job.Spec.Template.Spec.Containers[0].Env = append(
		job.Spec.Template.Spec.Containers[0].Env,
		corev1.EnvVar{
			Name:  "TF_PLUGIN_CACHE_DIR",
			Value: "/plugin-cache",
		},
	)
	if moduleCache {
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			corev1.VolumeMount{
				Name:      "plugin-cache",
				MountPath: "/work/.terraform/modules",
				SubPath:   "modules",
			},
		)
	}
}

// addEnvToJob injects user-specified env vars and envFrom into the first container of a Job.
func addEnvToJob(job *batchv1.Job, project *tofuv1alpha1.TofuProject) {
	if len(project.Spec.Env) > 0 {
		job.Spec.Template.Spec.Containers[0].Env = append(
			job.Spec.Template.Spec.Containers[0].Env,
			project.Spec.Env...,
		)
	}
	if len(project.Spec.EnvFrom) > 0 {
		job.Spec.Template.Spec.Containers[0].EnvFrom = append(
			job.Spec.Template.Spec.Containers[0].EnvFrom,
			project.Spec.EnvFrom...,
		)
	}
}

// addExtraVolumesToJob appends user-specified extra volumes and volume mounts to a Job.
func addExtraVolumesToJob(job *batchv1.Job, project *tofuv1alpha1.TofuProject) {
	if len(project.Spec.ExtraVolumes) > 0 {
		job.Spec.Template.Spec.Volumes = append(
			job.Spec.Template.Spec.Volumes,
			project.Spec.ExtraVolumes...,
		)
	}
	if len(project.Spec.ExtraVolumeMounts) > 0 {
		job.Spec.Template.Spec.Containers[0].VolumeMounts = append(
			job.Spec.Template.Spec.Containers[0].VolumeMounts,
			project.Spec.ExtraVolumeMounts...,
		)
	}
}

// addResourcesToJob sets resource requests/limits on the first container of a Job.
func addResourcesToJob(job *batchv1.Job, project *tofuv1alpha1.TofuProject) error {
	if project.Spec.Resources == nil {
		return nil
	}
	res := corev1.ResourceRequirements{}
	if len(project.Spec.Resources.Limits) > 0 {
		res.Limits = corev1.ResourceList{}
		for k, v := range project.Spec.Resources.Limits {
			qty, err := resource.ParseQuantity(v)
			if err != nil {
				return fmt.Errorf("parsing resource limit %s=%s: %w", k, v, err)
			}
			res.Limits[corev1.ResourceName(k)] = qty
		}
	}
	if len(project.Spec.Resources.Requests) > 0 {
		res.Requests = corev1.ResourceList{}
		for k, v := range project.Spec.Resources.Requests {
			qty, err := resource.ParseQuantity(v)
			if err != nil {
				return fmt.Errorf("parsing resource request %s=%s: %w", k, v, err)
			}
			res.Requests[corev1.ResourceName(k)] = qty
		}
	}
	job.Spec.Template.Spec.Containers[0].Resources = res
	return nil
}
