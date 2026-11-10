/**
 * Copyright (c) 2026 OpenBao a Series of LF Projects, LLC
 * SPDX-License-Identifier: MPL-2.0
 */

import { encodePath } from 'vault/utils/path-encoding-helpers';

// Thin client for the credential-sets API of the mounted engine. All requests
// go through the application adapter so token and namespace headers are added.
export default class CredentialSetApi {
  constructor(store, backend) {
    this.adapter = store.adapterFor('application');
    this.base = `/v1/${encodePath(backend)}/credential-sets`;
  }

  url(name, sub) {
    return [this.base, name && encodePath(name), sub].filter(Boolean).join('/');
  }

  async list() {
    try {
      const resp = await this.adapter.ajax(this.url(), 'GET', { data: { list: true } });
      return resp.data.keys || [];
    } catch (error) {
      if (error.httpStatus === 404) {
        return [];
      }
      throw error;
    }
  }

  async read(name) {
    return (await this.adapter.ajax(this.url(name), 'GET')).data;
  }

  async status(name) {
    return (await this.adapter.ajax(this.url(name, 'status'), 'GET')).data;
  }

  async creds(name) {
    return (await this.adapter.ajax(this.url(name, 'creds'), 'GET')).data;
  }

  create(name, data) {
    return this.adapter.ajax(this.url(name), 'POST', { data });
  }

  rotate(name) {
    return this.adapter.ajax(this.url(name, 'rotate'), 'POST');
  }

  remove(name) {
    return this.adapter.ajax(this.url(name), 'DELETE');
  }
}
