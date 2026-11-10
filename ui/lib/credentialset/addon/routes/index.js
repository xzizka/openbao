/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import Route from '@ember/routing/route';
import { inject as service } from '@ember/service';

export default class CredentialSetRoute extends Route {
  @service 'host-router';

  redirect() {
    this['host-router'].transitionTo('vault.cluster.secrets.backend.credentialset.overview');
  }
}
