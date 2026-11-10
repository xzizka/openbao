/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import Route from '@ember/routing/route';
import { inject as service } from '@ember/service';
import CredentialSetApi from '../utils/api';

export default class CredentialSetShowRoute extends Route {
  @service store;
  @service secretMountPath;

  async model({ name }) {
    const backend = this.secretMountPath.get();
    const api = new CredentialSetApi(this.store, backend);
    const [config, status] = await Promise.all([api.read(name), api.status(name)]);
    return { backend, name, config, status };
  }

  setupController(controller, resolvedModel) {
    super.setupController(controller, resolvedModel);
    controller.breadcrumbs = [
      { label: 'secrets', route: 'secrets', linkExternal: true },
      { label: resolvedModel.backend, route: 'overview', models: [resolvedModel.backend] },
      { label: resolvedModel.name },
    ];
  }
}
