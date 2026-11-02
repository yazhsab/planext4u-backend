import {act,render,screen} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import {afterEach,expect,it,vi} from "vitest";
import {RegionalSummaryExport} from "./regional-summary-export";
import {getRegionalSummaryCSV} from "../api/fulfillment";
import {adminSession} from "../test/fixtures";

afterEach(()=>{vi.unstubAllGlobals();vi.restoreAllMocks();});
const session={...adminSession,capabilities:[...adminSession.capabilities,"admin.fulfillment.export"]};
const csv=()=>new Response("Country,Region\r\nIN,NORTH\r\n",{headers:{"Content-Type":"text/csv; charset=utf-8"}});
it("prepares a bounded server CSV and releases its download when its account scope ends",async()=>{
 const create=vi.fn(()=>"blob:owned-regional-export"),revoke=vi.fn();
 vi.stubGlobal("URL",class extends URL{static override createObjectURL=create;static override revokeObjectURL=revoke;});
 const fetch=vi.fn(()=>Promise.resolve(csv()));vi.stubGlobal("fetch",fetch);
 const shown=render(<RegionalSummaryExport session={session} region="NORTH" onDenied={vi.fn()} />);
 await userEvent.click(screen.getByRole("button",{name:"Prepare delivery summary (CSV)"}));
 expect(await screen.findByRole("link",{name:"Download delivery summary CSV"})).toHaveAttribute("download","delivery-summary.csv");
 expect(fetch.mock.calls[0]).toEqual(["/admin/api/v1/fulfillment/regions/NORTH/summary-export",expect.objectContaining({credentials:"same-origin",cache:"no-store",redirect:"error"})]);
 shown.unmount();expect(revoke).toHaveBeenCalledWith("blob:owned-regional-export");
});
it("does not offer an export without server capability or fresh authentication",()=>{
 const shown=render(<RegionalSummaryExport session={adminSession} region="NORTH" onDenied={vi.fn()} />);
 expect(screen.queryByRole("button")).not.toBeInTheDocument();
 shown.rerender(<RegionalSummaryExport session={{...session,assurance:{...session.assurance,fresh_auth:false}}} region="NORTH" onDenied={vi.fn()} />);
 expect(screen.getByRole("button")).toBeDisabled();expect(screen.getByRole("link",{name:"Sign in again before exporting"})).toHaveAttribute("href","/login?reauth=mfa");
});
it("never offers a download after the owner denies the export",async()=>{
 vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(new Response("Denied",{status:403}))));const denied=vi.fn();
 render(<RegionalSummaryExport session={session} region="NORTH" onDenied={denied} />);
 await userEvent.click(screen.getByRole("button"));await screen.findByRole("alert");expect(denied).toHaveBeenCalledOnce();expect(screen.queryByRole("link")).not.toBeInTheDocument();
});
it("drops a late response after the scoped control is removed",async()=>{
 let finish:(value:Response)=>void=()=>{throw new Error("The export request has not started");};const create=vi.fn();
 vi.stubGlobal("URL",class extends URL{static override createObjectURL=create;});
 vi.stubGlobal("fetch",vi.fn(()=>new Promise<Response>(resolve=>{finish=resolve;})));
 const shown=render(<RegionalSummaryExport session={session} region="NORTH" onDenied={vi.fn()} />);await userEvent.click(screen.getByRole("button"));shown.unmount();await act(async()=>{finish(csv());await Promise.resolve();});
 expect(create).not.toHaveBeenCalled();
});
it("drops an in-flight result when permission changes on the same mounted account",async()=>{
 let finish:(value:Response)=>void=()=>{throw new Error("The export request has not started");};const create=vi.fn();
 vi.stubGlobal("URL",class extends URL{static override createObjectURL=create;});
 vi.stubGlobal("fetch",vi.fn(()=>new Promise<Response>(resolve=>{finish=resolve;})));
 const shown=render(<RegionalSummaryExport session={session} region="NORTH" onDenied={vi.fn()} />);await userEvent.click(screen.getByRole("button"));
 shown.rerender(<RegionalSummaryExport session={adminSession} region="NORTH" onDenied={vi.fn()} />);await act(async()=>{finish(csv());await Promise.resolve();});
 shown.rerender(<RegionalSummaryExport session={session} region="NORTH" onDenied={vi.fn()} />);
 expect(create).not.toHaveBeenCalled();expect(screen.queryByRole("link")).not.toBeInTheDocument();
});
it("rejects foreign content and oversized responses rather than creating a download",async()=>{
 for(const response of [Response.json({country:"IN"}),new Response("x".repeat(65537),{headers:{"Content-Type":"text/csv"}})]){
  vi.stubGlobal("fetch",vi.fn(()=>Promise.resolve(response)));
  await expect(getRegionalSummaryCSV("NORTH",new AbortController().signal)).rejects.toThrow();
 }
});
