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
 * @module CredentialSetCreate
 * Form that creates a credential set. Sets are immutable after creation.
 *
 * @param {object} model - { backend }
 * @param {array} breadcrumbs - breadcrumbs as an array of objects that contain label and route
 */
export default class CredentialSetCreateComponent extends Component {
  @service store;
  @service 'host-router';
  @service flashMessages;

  @tracked name = '';
  @tracked slots = '';
  @tracked validity = '30d';
  @tracked overlap = '15d';
  @tracked prepareLead = '';
  @tracked anchor = '';
  @tracked passwordLength = '32';
  @tracked error;

  @action
  update(field, event) {
    this[field] = event.target.value;
  }

  get slotList() {
    return this.slots
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
  }

  // The same relations the engine validates; shown as a hint before submitting.
  get hint() {
    const n = this.slotList.length;
    if (n < 2) {
      return 'At least 2 slots are required.';
    }
    return `${n} slots, one active at a time; each credential stays valid for the validity period.`;
  }

  @task
  @waitFor
  *save(event) {
    event.preventDefault();
    this.error = null;
    if (!this.name.trim() || this.slotList.length < 2 || !this.validity.trim()) {
      this.error = 'Name, at least 2 slots and the validity are required.';
      return;
    }
    const data = {
      slots: this.slotList.join(','),
      validity: this.validity.trim(),
      overlap: this.overlap.trim() || '0s',
    };
    if (this.prepareLead.trim()) data.prepare_lead = this.prepareLead.trim();
    if (this.anchor.trim()) data.anchor = this.anchor.trim();
    if (this.passwordLength.trim()) data.password_length = Number(this.passwordLength);
    try {
      const api = new CredentialSetApi(this.store, this.args.model.backend);
      yield api.create(this.name.trim(), data);
      this.flashMessages.success(`Created credential set ${this.name.trim()}`);
      this['host-router'].transitionTo(
        'vault.cluster.secrets.backend.credentialset.set',
        this.name.trim()
      );
    } catch (error) {
      this.error = errorMessage(error, 'Error creating credential set.');
    }
  }

  @action
  cancel() {
    this['host-router'].transitionTo('vault.cluster.secrets.backend.credentialset.overview');
  }
}
