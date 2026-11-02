import {render,screen,waitFor} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach,expect,it,vi} from "vitest";
import {FieldCheckIn} from "./field-check-in";
import {adminSession} from "../test/fixtures";

const id="00000000-0000-4000-8000-000000000001";
const original=Object.getOwnPropertyDescriptor(navigator,"geolocation");
afterEach(()=>{vi.unstubAllGlobals();if(original) Object.defineProperty(navigator,"geolocation",original);else Reflect.deleteProperty(navigator,"geolocation");});
const visit={id:"visit-001",officer_id:adminSession.subject_id,territory_id:id,point:{latitude:13.08,longitude:80.27},distance_m:0,recorded_at:"2026-10-25T09:00:00Z"};
function location() {
  let success: PositionCallback | undefined;
  let fail: PositionErrorCallback | null | undefined;
  const get=vi.fn<Geolocation["getCurrentPosition"]>((yes,no)=>{success=yes;fail=no;});
  Object.defineProperty(navigator,"geolocation",{configurable:true,value:{getCurrentPosition:get}});
  return {get,succeed:()=>success?.({coords:{latitude:13.08,longitude:80.27,accuracy:5},timestamp:Date.now()} as GeolocationPosition),deny:()=>fail?.({code:1,message:"denied"} as GeolocationPositionError)};
}
function show(fresh=true) {
  const onRecorded=vi.fn(),onDenied=vi.fn();
  const view=render(<FieldCheckIn session={{...adminSession,assurance:{...adminSession.assurance,fresh_auth:fresh}}} territories={[{id,name:"North deliveries"}]} onRecorded={onRecorded} onDenied={onDenied} />);
  return {...view,onRecorded,onDenied};
}
async function capture(current:ReturnType<typeof location>){await userEvent.click(screen.getByRole("button",{name:"Use current location"}));current.succeed();await screen.findByText("Approximate accuracy: 5 m");}

it("captures only on request and submits the point with CSRF and an idempotency reference",async()=>{
  const current=location();const fetch=vi.fn(()=>Promise.resolve(Response.json(visit)));vi.stubGlobal("fetch",fetch);
  const shown=show();expect(current.get).not.toHaveBeenCalled();
  await capture(current);await userEvent.click(screen.getByRole("button",{name:"Confirm location and record visit"}));
  await screen.findByText("Field visit recorded.");expect(shown.onRecorded).toHaveBeenCalledTimes(1);
  const [path,request]=fetch.mock.calls[0] as unknown as [string,RequestInit];
  expect(path).toBe(`/admin/api/v1/fulfillment/territories/${id}/field-check-ins`);
  expect(JSON.parse(request.body as string)).toEqual({point:visit.point});
  const headers=new Headers(request.headers);expect(headers.get("X-CSRF-Token")).toBe(adminSession.csrf_token);expect(headers.get("Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);expect(headers.has("Authorization")).toBe(false);
});

it("retries an unconfirmed visit with the identical point and reference",async()=>{
  const current=location();const requests:RequestInit[]=[];
  vi.stubGlobal("fetch",vi.fn((_path:string,request:RequestInit)=>{requests.push(request);return Promise.resolve(requests.length===1?Response.json({error:{code:"UNAVAILABLE",message:"Unavailable",correlation_id:"test",retryable:true}},{status:503}):Response.json(visit));}));
  show();await capture(current);await userEvent.click(screen.getByRole("button",{name:"Confirm location and record visit"}));
  await screen.findByRole("alert");await userEvent.click(screen.getByRole("button",{name:"Retry the same visit"}));
  await screen.findByText("Field visit recorded.");expect(requests).toHaveLength(2);
  const [first,second]=requests;if(!first || !second) throw new Error("Missing retry requests");
  expect(second.body).toBe(first.body);expect(new Headers(second.headers).get("Idempotency-Key")).toBe(new Headers(first.headers).get("Idempotency-Key"));
});

it("permission denial sends no location or command",async()=>{
  const current=location();const fetch=vi.fn();vi.stubGlobal("fetch",fetch);show();
  await userEvent.click(screen.getByRole("button",{name:"Use current location"}));current.deny();
  await screen.findByText(/Location permission was denied/);expect(fetch).not.toHaveBeenCalled();
});

it("an unavailable location refresh cannot retain the previous point",async()=>{
  const current=location();const fetch=vi.fn();vi.stubGlobal("fetch",fetch);show();await capture(current);
  Object.defineProperty(navigator,"geolocation",{configurable:true,value:undefined});
  await userEvent.click(screen.getByRole("button",{name:"Use current location"}));
  await screen.findByText(/A current location could not be obtained/);
  expect(screen.queryByRole("button",{name:"Confirm location and record visit"})).not.toBeInTheDocument();expect(fetch).not.toHaveBeenCalled();
});

it("clears location and refreshes current scope after an owning-service denial",async()=>{
  const current=location();vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(Response.json({error:{code:"DENIED",message:"Denied",correlation_id:"test",retryable:false}},{status:403}))));
  const shown=show();await capture(current);await userEvent.click(screen.getByRole("button",{name:"Confirm location and record visit"}));
  await screen.findByRole("alert");expect(shown.onDenied).toHaveBeenCalledTimes(1);expect(screen.queryByRole("button",{name:"Confirm location and record visit"})).not.toBeInTheDocument();expect(screen.queryByText("Approximate accuracy: 5 m")).not.toBeInTheDocument();
});

it("drops late location callbacks when the scope/account view is removed",async()=>{
  const current=location();const fetch=vi.fn();vi.stubGlobal("fetch",fetch);const shown=show();
  await userEvent.click(screen.getByRole("button",{name:"Use current location"}));shown.unmount();current.succeed();expect(fetch).not.toHaveBeenCalled();
});

it("requires fresh authentication and supports cancelling an in-flight capture",async()=>{
  const current=location();const shown=show(false);
  expect(screen.getByRole("button",{name:"Use current location"})).toBeDisabled();expect(screen.getByRole("link",{name:/Re-authenticate/})).toHaveAttribute("href","/login?reauth=mfa");shown.unmount();
  show();await userEvent.click(screen.getByRole("button",{name:"Use current location"}));await userEvent.click(screen.getByRole("button",{name:"Clear location and start again"}));current.succeed();
  await waitFor(()=>{expect(screen.queryByText("Approximate accuracy: 5 m")).not.toBeInTheDocument();});
});
