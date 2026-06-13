import {test} from '@playwright/test';
import {installVirtualAuthenticator} from '../helpers/webauthn.js';
import {
  acceptFirstWaitingWorker,
  bootstrapFirstUser,
  createNixDockerDeployment,
  signOutAndLoginWithPasskey,
} from '../helpers/ui.js';

test('bootstrap primary, enroll worker, and create Nix Docker deployment', async ({page}) => {
  await installVirtualAuthenticator(page);

  await bootstrapFirstUser(page);
  await signOutAndLoginWithPasskey(page);
  await acceptFirstWaitingWorker(page);
  await createNixDockerDeployment(page);
});
