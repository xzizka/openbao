/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import Component from '@glimmer/component';
import { getOwner } from '@ember/application';

/**
 * @module CredentialSetOverview
 * Lists the credential sets of the mount.
 *
 * @param {object} model - { backend, sets: [{ name, status | error }] }
 * @param {array} breadcrumbs - breadcrumbs as an array of objects that contain label and route
 */
export default class CredentialSetOverviewComponent extends Component {
  get mountPoint() {
    return getOwner(this).mountPoint;
  }
}
