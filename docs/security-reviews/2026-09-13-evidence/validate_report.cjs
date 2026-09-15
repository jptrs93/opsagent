// Run from the repository root; uses the existing E2E Playwright installation.
const {chromium}=require('../../../testing-vms/e2e/node_modules/@playwright/test');
const fs=require('fs'),path=require('path');
const root=path.resolve(__dirname,'../../..');
const report=path.join(root,'docs/security-reviews/2026-09-13-opendeploy-security-re-review.html');
const data=JSON.parse(fs.readFileSync(path.join(__dirname,'findings.json'),'utf8'));
const total=data.findings.length;
const bySev=s=>data.findings.filter(f=>f.severity.toLowerCase()===s).length;
(async()=>{
 const browser=await chromium.launch({headless:true});
 try {
  const page=await browser.newPage({viewport:{width:1440,height:1000}});
  const errors=[],remote=[];
  page.on('pageerror',x=>errors.push(x.message));
  page.on('request',r=>{if(/^https?:/.test(r.url()))remote.push(r.url())});
  await page.goto('file://'+report);
  const desktop=await page.evaluate(()=>({width:innerWidth,scrollWidth:document.documentElement.scrollWidth,findings:document.querySelectorAll('.finding').length}));
  if(desktop.findings!==total)throw Error('Finding count '+desktop.findings+' != '+total);
  await page.screenshot({path:path.join(__dirname,'report-desktop.png')});
  for(const sev of ['high','medium','low']){
   await page.selectOption('#severity',sev);
   const n=await page.locator('.finding:visible').count();
   if(n!==bySev(sev))throw Error(sev+' filter: '+n+' != '+bySev(sev));
  }
  await page.selectOption('#severity','');
  await page.fill('#search','not-a-finding-abcdef');
  if(!await page.locator('#empty').isVisible())throw Error('Empty state failed');
  await page.evaluate(()=>location.hash='OD-07');
  await page.waitForFunction(()=>!document.getElementById('OD-07').hidden);
  await page.fill('#search','duplicate credentials');
  if(await page.locator('.finding:visible').count()!==1)throw Error('Search failed');
  await page.fill('#search','');
  const links=await page.evaluate(()=>Array.from(document.querySelectorAll('a[href]')).map(a=>a.getAttribute('href')));
  for(const href of links){
   if(href.startsWith('#')){if(!await page.locator(href).count())throw Error('Missing anchor '+href)}
   else if(!/^[a-z]+:/.test(href)&&!fs.existsSync(path.resolve(path.dirname(report),href)))throw Error('Missing local link '+href)
  }
  await page.selectOption('#severity','high');
  await page.emulateMedia({media:'print'});
  const printVisible=await page.locator('.finding:visible').count();
  if(printVisible!==total)throw Error('Print should include every finding: '+printVisible);
  await page.emulateMedia({media:'screen'});
  await page.selectOption('#severity','');
  await page.setViewportSize({width:390,height:844});
  await page.evaluate(()=>{location.hash='';scrollTo({top:0,behavior:'instant'})});
  await page.screenshot({path:path.join(__dirname,'report-mobile.png')});
  const mobile=await page.evaluate(()=>({width:innerWidth,scrollWidth:document.documentElement.scrollWidth}));
  if(mobile.scrollWidth>mobile.width||desktop.scrollWidth>desktop.width)throw Error('Page overflow');
  if(errors.length||remote.length)throw Error('Page errors or external network requests: '+JSON.stringify({errors,remote}));
  const result={desktop,mobile,printVisible,filters:'passed',search:'passed',anchorReset:'passed',localLinks:'passed',pageErrors:errors,remoteRequests:remote};
  fs.writeFileSync(path.join(__dirname,'html-validation.json'),JSON.stringify(result,null,2)+'\n');
  console.log(JSON.stringify(result));
 } finally {await browser.close()}
})().catch(e=>{console.error(e);process.exitCode=1});
