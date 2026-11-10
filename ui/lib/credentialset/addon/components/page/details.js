/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import Component from '@glimmer/component';
import { inject as service } from '@ember/service';
import { tracked } from '@glimmer/tracking';
import { action } from '@ember/object';
import { task } from 'ember-concurrency';
import { waitFor } from '@ember/test-waiters';
import errorMessage from 'vault/utils/error-message';
import CredentialSetApi from '../../utils/api';

/**
 * @module CredentialSetDetails
 * Shows configuration and slot states of a set, rotates it, deletes it and
 * reveals the credential that is currently served.
 *
 * @param {object} model - { backend, name, config, status }
 * @param {array} breadcrumbs - breadcrumbs as an array of objects that contain label and route
 */
export default class CredentialSetDetailsComponent extends Component {
  @service store;
  @service 'host-router';
  @service flashMessages;

  @tracked status = null;
  @tracked credentials = null;
  @tracked error;

  get api() {
    return new CredentialSetApi(this.store, this.args.model.backend);
  }

  get current() {
    return this.status || this.args.model.status;
  }

  get config() {
    return this.args.model.config;
  }

  @task
  @waitFor
  *refresh() {
    try {
      this.status = yield this.api.status(this.args.model.name);
    } catch (error) {
      this.error = errorMessage(error);
    }
  }

  @task
  @waitFor
  *reveal() {
    this.error = null;
    try {
      this.credentials = yield this.api.creds(this.args.model.name);
      yield this.refresh.perform();
    } catch (error) {
      this.error = errorMessage(error);
    }
  }

  @task
  @waitFor
  *rotate() {
    this.error = null;
    try {
      yield this.api.rotate(this.args.model.name);
      this.credentials = null;
      this.flashMessages.success('Rotated: the next slot is now active.');
      yield this.refresh.perform();
    } catch (error) {
      this.error = errorMessage(error, 'Error rotating credential set.');
    }
  }

  @task
  @waitFor
  *delete() {
    try {
      yield this.api.remove(this.args.model.name);
      this.flashMessages.success(`Deleted credential set ${this.args.model.name}`);
      this['host-router'].transitionTo('vault.cluster.secrets.backend.credentialset.overview');
    } catch (error) {
      this.error = errorMessage(error, 'Error deleting credential set.');
    }
  }

  @action
  hide() {
    this.credentials = null;
  }
}
