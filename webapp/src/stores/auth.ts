import { defineStore } from 'pinia';
import { ref, computed } from 'vue';
import { api } from '../api';

export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(localStorage.getItem('token'));
  const isInitialized = ref<boolean>(false);
  const loading = ref<boolean>(false);
  const error = ref<string | null>(null);
  const tokens = ref<any[]>([]);

  const isLoggedIn = computed(() => !!token.value);

  function setToken(newToken: string) {
    token.value = newToken;
    localStorage.setItem('token', newToken);
  }

  function logout() {
    token.value = null;
    localStorage.removeItem('token');
  }

  async function checkStatus() {
    try {
      loading.value = true;
      const res = await api.get('auth/status');
      isInitialized.value = res.data.initialized;
    } catch (err: any) {
      console.error('Failed to check status', err);
    } finally {
      loading.value = false;
    }
  }

  async function fetchTokens() {
    try {
      const res = await api.get('auth/tokens');
      tokens.value = res.data || [];
    } catch (e: any) {
      error.value = e.message;
    }
  }

  async function generateToken(name: string, deviceId?: number) {
    try {
      const res = await api.post('auth/tokens', {
        name,
        device_id: deviceId || undefined,
      });
      await fetchTokens();
      return res.data.token;
    } catch (e: any) {
      throw e;
    }
  }

  async function updateTokenDevice(id: number, deviceId: number | null) {
    try {
      await api.put(`auth/tokens/${id}`, { device_id: deviceId });
      await fetchTokens();
    } catch (e: any) {
      throw e;
    }
  }

  async function revokeToken(id: number) {
    try {
      await api.delete(`auth/tokens/${id}`);
      await fetchTokens();
    } catch (e: any) {
      throw e;
    }
  }

  return {
    token,
    tokens,
    isInitialized,
    isLoggedIn,
    loading,
    error,
    setToken,
    logout,
    checkStatus,
    fetchTokens,
    generateToken,
    updateTokenDevice,
    revokeToken,
  };
});
