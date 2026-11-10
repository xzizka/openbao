/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import Route from '@ember/routing/route';
import { inject as service } from '@ember/service';
import CredentialSetApi from '../utils/api';

export default class CredentialSetOverviewRoute extends Route {
  @service store;
  @service secretMountPath;

  async model() {
    const backend = this.secretMountPath.get();
    const api = new CredentialSetApi(this.store, backend);
    const names = await api.list();
    // the status endpoint never returns secrets; a failing set must not hide the others
    const sets = await Promise.all(
      names.map(async (name) => {
        try {
          return { name, status: await api.status(name) };
        } catch (error) {
          return { name, error: error.message };
        }
      })
    );
    return { backend, sets };
  }

  setupController(controller, resolvedModel) {
    super.setupController(controller, resolvedModel);
    controller.breadcrumbs = [
      { label: 'secrets', route: 'secrets', linkExternal: true },
      { label: resolvedModel.backend },
    ];
  }
}
