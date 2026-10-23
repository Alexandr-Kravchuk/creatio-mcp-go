#pragma warning disable CS8618, // Non-nullable field is uninitialized.

using ATF.Repository;
using ATF.Repository.Attributes;
using System;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;
using System.ComponentModel.DataAnnotations;

namespace Acme.Models {
	/// <summary>
	/// Phones, e-mails and other ways to reach a contact
	/// </summary>
	[ExcludeFromCodeCoverage]
	[Schema("ContactCommunication")]
	public class ContactCommunication: BaseModel {

		#region Schema Columns

		/// <summary>
		/// Contact
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[SchemaProperty("Contact")]
		public Guid ContactId { get; set; }

		/// <inheritdoc cref="ContactId"/>
		[LookupProperty("Contact")]
		public virtual Contact Contact { get; set; }

		/// <summary>
		/// Number
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[MinLength(1, ErrorMessage = "The {0} field must be a minimum of {1} character long.")]
		[MaxLength(250, ErrorMessage = "The {0} field must be a maximum of {1} character long.")]
		[SchemaProperty("Number")]
		public string Number { get; set; }

		/// <summary>
		/// Type
		/// </summary>
		[Required(ErrorMessage = "The {0} field is required.")]
		[SchemaProperty("CommunicationType")]
		public Guid CommunicationTypeId { get; set; }

		/// <inheritdoc cref="CommunicationTypeId"/>
		[LookupProperty("CommunicationType")]
		public virtual CommunicationType CommunicationType { get; set; }

		[SchemaProperty("Primary")]
		public bool Primary { get; set; }

		/// <summary>
		/// Position
		/// </summary>
		[SchemaProperty("Position")]
		public int Position { get; set; }

		/// <summary>
		/// Account
		/// </summary>
		[SchemaProperty("UsrAccount")]
		public Guid UsrAccountId { get; set; }

		/// <inheritdoc cref="UsrAccountId"/>
		[LookupProperty("UsrAccount")]
		public virtual Account UsrAccount { get; set; }

		/// <summary>
		/// Account (legacy)
		/// </summary>
		[SchemaProperty("Usr_Account")]
		public Guid Usr_AccountId { get; set; }

		/// <inheritdoc cref="Usr_AccountId"/>
		[LookupProperty("Usr_Account")]
		public virtual Account Usr_Account { get; set; }

		[SchemaProperty("Extra")]
		public string ExtraId { get; set; }

		[LookupProperty("Extra")]
		public virtual   Extra { get; set; }

		[SchemaProperty("usrAccountLink")]
		public Guid usrAccountLinkId { get; set; }

		[LookupProperty("usrAccountLink")]
		public virtual Account usrAccountLink { get; set; }

		#endregion

	}
}
#pragma warning restore CS8618 // Non-nullable field is uninitialized.
