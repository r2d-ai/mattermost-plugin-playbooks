// Copyright (c) 2020-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

// Optional browser check for the fixed disposable host created by run.sh.
const assert = require('node:assert/strict');
const {chromium} = require('../../e2e-tests/playwright/node_modules/playwright');

let browser;
let page;

(async () => {
    const runID = process.argv[2];
    const username = process.argv[3] || 'office-smoke-admin';
    assert.match(runID || '', /^[a-z0-9]{26}$/, 'A smoke run ID is required');
    assert.match(username, /^office-(smoke-admin|reviewer-[a-f0-9]{8})$/, 'Use a disposable smoke account');
    const base = 'http://127.0.0.1:18065';
    const role = username === 'office-smoke-admin' ? '' : '-reviewer';
    browser = await chromium.launch({executablePath: '/usr/bin/chromium', headless: true, args: ['--no-sandbox']});
    const context = await browser.newContext({viewport: {width: 1440, height: 1050}});
    const login = await context.request.post(base + '/api/v4/users/login', {
        data: {login_id: username, password: 'OfficeSmoke-Only123!'},
    });
    assert(login.ok(), 'Smoke account login failed');
    const account = await login.json();
    await context.addCookies([
        {name: 'MMAUTHTOKEN', value: login.headers().token, url: base},
        {name: 'MMUSERID', value: account.id, url: base},
    ]);
    const gateResponse = await context.request.get(base + '/plugins/playbooks/api/v0/runs/' + runID + '/approval-gates');
    assert(gateResponse.ok(), 'The account must have access to the smoke run');
    const [gate] = await gateResponse.json();
    assert(gate, 'The smoke run must contain an approval gate');
    const action = gate.status === 'pending' && gate.can_review ? 'Review' : 'View request';
    const statusLabel = gate.status[0].toUpperCase() + gate.status.slice(1);
    const verifyRegion = async (region) => {
        await region.getByText(gate.title, {exact: true}).waitFor({timeout: 60000});
        await region.getByText(statusLabel, {exact: true}).waitFor({timeout: 60000});
        const link = region.getByRole('link', {name: action, exact: true});
        assert.equal(await link.getAttribute('href'), gate.path);
        assert.equal(await region.getByRole('checkbox').count(), 0);
        await region.scrollIntoViewIfNeeded();
    };

    page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(base + '/playbooks/runs/' + runID);
    const browserChoice = page.getByText('View in Browser', {exact: true});
    const backstage = page.getByRole('region', {name: 'Office approvals', exact: true});
    await Promise.race([
        browserChoice.waitFor({timeout: 60000}),
        backstage.waitFor({timeout: 60000}),
    ]);
    if (await browserChoice.isVisible()) {
        await browserChoice.click();
    }
    await verifyRegion(backstage);
    console.log('BACKSTAGE', await backstage.innerText());
    await page.screenshot({path: '/tmp/playbooks-office-backstage' + role + '.png', fullPage: true});

    const run = await (await context.request.get(base + '/plugins/playbooks/api/v0/runs/' + runID)).json();
    const channel = await (await context.request.get(base + '/api/v4/channels/' + run.channel_id)).json();
    const team = await (await context.request.get(base + '/api/v4/teams/' + channel.team_id)).json();
    await page.goto(base + '/' + team.name + '/channels/' + channel.name);
    await page.getByText(channel.header, {exact: true}).first().waitFor({timeout: 60000});
    const onboardingDismiss = page.getByText(/No thanks, I.ll figure it out myself/);
    try {
        await onboardingDismiss.waitFor({timeout: 5000});
        await onboardingDismiss.click();
    } catch (error) {
        if (error.name !== 'TimeoutError') {
            throw error;
        }
    }
    const headerIcon = page.locator('#incidentIcon, #app-bar-icon-playbooks');
    const rhs = page.getByRole('region', {name: 'Office approvals', exact: true});
    await Promise.race([
        rhs.waitFor({timeout: 60000}),
        headerIcon.waitFor({timeout: 60000}),
    ]);
    if (!(await rhs.isVisible())) {
        await headerIcon.click();
    }
    await verifyRegion(rhs);
    if (await onboardingDismiss.isVisible()) {
        await onboardingDismiss.click();
    }
    await page.mouse.move(700, 400);
    console.log('RHS', await rhs.innerText());
    await page.screenshot({path: '/tmp/playbooks-office-rhs' + role + '.png', fullPage: true});
    assert.deepEqual(errors, [], 'The browser must not report JavaScript page errors');
    console.log(JSON.stringify({run_id: runID, username, gate_status: gate.status, action, result: 'passed'}));
    await browser.close();
})().catch(async error => {
    console.error(error);
    if (page) {
        console.log('URL', page.url());
        console.log('BODY', (await page.locator('body').innerText({timeout: 5000}).catch(() => 'unavailable')).slice(0, 8000));
        await page.screenshot({path: '/tmp/playbooks-office-browser-failure.png', fullPage: true, timeout: 5000}).catch(() => {});
    }
    if (browser) {
        await browser.close();
    }
    process.exitCode = 1;
});
