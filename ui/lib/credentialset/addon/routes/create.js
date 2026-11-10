/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import Route from '@ember/routing/route';
import { inject as service } from '@ember/service';

export default class CredentialSetCreateRoute extends Route {
  @service secretMountPath;

  model() {
    return { backend: this.secretMountPath.get() };
  }

  setupController(controller, resolvedModel) {
    super.setupController(controller, resolvedModel);
    controller.breadcrumbs = [
      { label: 'secrets', route: 'secrets', linkExternal: true },
      { label: resolvedModel.backend, route: 'overview', models: [resolvedModel.backend] },
      { label: 'create' },
    ];
  }
}
