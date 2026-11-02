import {useInfiniteQuery} from "@tanstack/react-query";
import {getAudienceGroups} from "../api/audiences";
import type {AdminSession} from "../api/client";

export function useAudienceDirectory(session:AdminSession){
 const query=useInfiniteQuery({
  queryKey:["admin","audience-pages",session.subject_id,session.selected_country],
  initialPageParam:undefined as string|undefined,
  queryFn:({signal,pageParam})=>getAudienceGroups(signal,pageParam),
  getNextPageParam:last=>last.next_cursor,
  retry:false,
 });
 // A revoked/failed page must not leave a partial directory presented as current.
 const groups=query.isSuccess ? [...new Map(query.data.pages.flatMap(page=>page.groups).map(group=>[group.id,group])).values()] : [];
 return {query,groups};
}
